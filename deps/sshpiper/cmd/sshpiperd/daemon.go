package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tg123/sshpiper/cmd/sshpiperd/internal/plugin"
	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/ssh"
)

type daemon struct {
	config         *plugin.GrpcPluginConfig
	lis            net.Listener
	loginGraceTime time.Duration

	recorddir             string
	recordfmt             string
	usernameAsRecorddir   bool
	filterHostkeysReqeust bool
	replyPing             bool

	// quit tracks and propagates exit errors from plugins.
	quit chan error

	// pendingPlugins holds configurations for plugins
	// that have not yet been initialized and installed.
	// It is set to nil once all plugins have been initialized and installed.
	pendingPlugins []*pluginConfig
}

type pluginConfig struct {
	// command line args to start the plugin.
	args []string
	// plugin is set once initialized.
	// any given plugin is initialized exactly once.
	plugin *plugin.GrpcPlugin
}

func generateSshKey(keyfile string) error {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	privateKeyPEM, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return err
	}

	privateKeyBytes := pem.EncodeToMemory(privateKeyPEM)

	return os.WriteFile(keyfile, privateKeyBytes, 0o600)
}

func newDaemon(ctx *cli.Context) (*daemon, error) {
	config := &plugin.GrpcPluginConfig{}

	config.Ciphers = ctx.StringSlice("allowed-downstream-ciphers-algos")
	config.MACs = ctx.StringSlice("allowed-downstream-macs-algos")
	config.KeyExchanges = ctx.StringSlice("allowed-downstream-keyexchange-algos")
	config.PublicKeyAuthAlgorithms = ctx.StringSlice("allowed-downstream-pubkey-algos")

	config.SetDefaults()

	// tricky, call SetDefaults, in first call, Cipers, Macs, Kex will be nil if [] and the second call will set the default values
	// this can be ignored because sshpiper.go will call SetDefaults again before use it
	// however, this is to make sure that the default values are set no matter sshiper.go calls SetDefaults or not
	config.SetDefaults()

	hostSigners := make([]ssh.Signer, 0)
	hostSignerByKey := make(map[string]ssh.Signer)

	addHostSigner := func(s ssh.Signer) {
		keyID := string(s.PublicKey().Marshal())
		hostSigners = append(hostSigners, s)
		hostSignerByKey[keyID] = s
	}

	keybase64 := ctx.String("server-key-data")
	if keybase64 != "" {
		log.Infof("parsing host key in base64 params")

		privateBytes, err := base64.StdEncoding.DecodeString(keybase64)
		if err != nil {
			return nil, err
		}

		private, err := ssh.ParsePrivateKey([]byte(privateBytes))
		if err != nil {
			return nil, err
		}

		addHostSigner(private)
	} else {
		keyfile := ctx.String("server-key")
		privateKeyFiles, err := filepath.Glob(keyfile)
		if err != nil {
			return nil, err
		}

		generate := false

		switch ctx.String("server-key-generate-mode") {
		case "notexist":
			generate = len(privateKeyFiles) == 0
		case "always":
			generate = true
		case "disable":
		default:
			return nil, fmt.Errorf("unknown server-key-generate-mode %v", ctx.String("server-key-generate-mode"))
		}

		if generate {
			log.Infof("generating host key %v", keyfile)
			if err := generateSshKey(keyfile); err != nil {
				return nil, err
			}

			privateKeyFiles = []string{keyfile}
		}

		if len(privateKeyFiles) == 0 {
			return nil, fmt.Errorf("no server key found")
		}

		log.Infof("found host keys %v", privateKeyFiles)
		for _, privateKey := range privateKeyFiles {
			log.Infof("loading host key %v", privateKey)
			privateBytes, err := os.ReadFile(privateKey)
			if err != nil {
				return nil, err
			}

			private, err := ssh.ParsePrivateKey(privateBytes)
			if err != nil {
				return nil, err
			}

			addHostSigner(private)
		}
	}

	if len(hostSigners) == 0 {
		return nil, fmt.Errorf("no server key loaded")
	}

	certSignersByKey := make(map[string][]ssh.Signer)

	loadCertificates := func(data []byte, source string) error {
		signers, err := certificateSignersFromData(data, source, hostSignerByKey)
		if err != nil {
			return err
		}

		for idx, entry := range signers {
			log.Infof("loading host certificate %v (entry %d)", source, idx+1)
			certSignersByKey[entry.hostKeyID] = append(certSignersByKey[entry.hostKeyID], entry.signer)
		}

		return nil
	}

	certBase64 := ctx.String("server-cert-data")
	if certBase64 != "" {
		log.Infof("parsing host certificate in base64 params")

		certBytes, err := base64.StdEncoding.DecodeString(certBase64)
		if err != nil {
			return nil, err
		}

		if err := loadCertificates(certBytes, "base64 parameter"); err != nil {
			return nil, err
		}
	}

	certPattern := ctx.String("server-cert")
	if certPattern != "" {
		certFiles, err := filepath.Glob(certPattern)
		if err != nil {
			return nil, err
		}

		if len(certFiles) == 0 {
			log.Warnf("no server certificate found matching pattern %v", certPattern)
		}

		for _, certFile := range certFiles {
			certBytes, err := os.ReadFile(certFile)
			if err != nil {
				return nil, err
			}

			if err := loadCertificates(certBytes, certFile); err != nil {
				return nil, err
			}
		}
	}

	for _, signer := range hostSigners {
		keyID := string(signer.PublicKey().Marshal())

		if certSigners := certSignersByKey[keyID]; len(certSigners) > 0 {
			for _, certSigner := range certSigners {
				config.AddHostKey(certSigner)
			}
		}

		config.AddHostKey(signer)
	}

	lis, err := net.Listen("tcp", net.JoinHostPort(ctx.String("address"), ctx.String("port")))
	if err != nil {
		return nil, fmt.Errorf("failed to listen for connection: %v", err)
	}

	bannertext := ctx.String("banner-text")
	bannerfile := ctx.String("banner-file")

	if bannertext != "" || bannerfile != "" {
		config.DownstreamBannerCallback = func(_ ssh.ConnMetadata, _ ssh.ChallengeContext) string {
			if bannerfile != "" {
				text, err := os.ReadFile(bannerfile)
				if err != nil {
					log.Warnf("cannot read banner file %v: %v", bannerfile, err)
				} else {
					return string(text)
				}
			}
			return bannertext
		}
	}

	switch ctx.String("upstream-banner-mode") {
	case "passthrough":
		// library will handle the banner to client
	case "ignore":
		config.UpstreamBannerCallback = func(_ ssh.ServerPreAuthConn, _ string, _ ssh.ChallengeContext) error {
			return nil
		}
	case "dedup":
		config.UpstreamBannerCallback = func(downstream ssh.ServerPreAuthConn, banner string, ctx ssh.ChallengeContext) error {
			meta, ok := ctx.Meta().(*plugin.PluginConnMeta)
			if !ok {
				// should not happen, but just in case
				log.Warnf("upstream banner deduplication failed, cannot get plugin connection meta from challenge context")
				return nil
			}

			hash := fmt.Sprintf("%x", md5.Sum([]byte(banner)))
			key := fmt.Sprintf("sshpiperd.upstream.banner.%s", hash)

			if meta.Metadata[key] == "true" {
				return nil
			}

			meta.Metadata[key] = "true"

			return downstream.SendAuthBanner(banner)
		}
	case "first-only":
		config.UpstreamBannerCallback = func(downstream ssh.ServerPreAuthConn, banner string, ctx ssh.ChallengeContext) error {
			meta, ok := ctx.Meta().(*plugin.PluginConnMeta)
			if !ok {
				// should not happen, but just in case
				log.Warnf("upstream banner first-only failed, cannot get plugin connection meta from challenge context")
				return nil
			}

			if meta.Metadata["sshpiperd.upstream.banner.sent"] == "true" {
				return nil
			}

			meta.Metadata["sshpiperd.upstream.banner.sent"] = "true"
			return downstream.SendAuthBanner(banner)
		}
	default:
		return nil, fmt.Errorf("unknown upstream banner mode %q; allowed: 'passthrough' or 'ignore'", ctx.String("upstream-banner-mode"))
	}

	return &daemon{
		config:         config,
		lis:            lis,
		loginGraceTime: ctx.Duration("login-grace-time"),
	}, nil
}

type hostCertificateSigner struct {
	hostKeyID string
	signer    ssh.Signer
}

func certificateSignersFromData(data []byte, source string, hostSignerByKey map[string]ssh.Signer) ([]hostCertificateSigner, error) {
	rest := bytes.TrimSpace(data)
	signers := make([]hostCertificateSigner, 0)

	for len(rest) > 0 {
		pub, _, _, remainder, err := ssh.ParseAuthorizedKey(rest)
		if err != nil {
			return nil, fmt.Errorf("failed to parse host certificate from %v: %w", source, err)
		}

		cert, ok := pub.(*ssh.Certificate)
		if !ok {
			return nil, fmt.Errorf("entry in %v is not an SSH certificate", source)
		}

		hostKeyID := string(cert.Key.Marshal())
		hostSigner, ok := hostSignerByKey[hostKeyID]
		if !ok {
			return nil, fmt.Errorf("no matching host key loaded for certificate in %v", source)
		}

		certCopy := *cert
		certSigner, err := ssh.NewCertSigner(&certCopy, hostSigner)
		if err != nil {
			return nil, fmt.Errorf("failed to build host certificate signer for %v: %w", source, err)
		}

		signers = append(signers, hostCertificateSigner{
			hostKeyID: hostKeyID,
			signer:    certSigner,
		})

		rest = bytes.TrimSpace(remainder)
	}

	if len(signers) == 0 {
		return nil, fmt.Errorf("no host certificate entries found in %v", source)
	}

	return signers, nil
}

func (d *daemon) install(plugins ...*plugin.GrpcPlugin) error {
	if len(plugins) == 0 {
		return fmt.Errorf("no plugins found")
	}

	if len(plugins) == 1 {
		return plugins[0].InstallPiperConfig(d.config)
	}

	m := plugin.ChainPlugins{}

	for _, p := range plugins {
		if err := m.Append(p); err != nil {
			return err
		}
	}

	return m.InstallPiperConfig(d.config)
}

// startPlugin starts a plugin from args.
// The caller is responsible for ensuring len(args) > 0.
// Plugin exit errors will be sent to quit channel.
func startPlugin(args []string, quit chan error) (*plugin.GrpcPlugin, error) {
	var p *plugin.GrpcPlugin

	switch args[0] {
	case "grpc":
		log.Info("starting net grpc plugin: ")

		grpcplugin, err := createNetGrpcPlugin(args)
		if err != nil {
			return nil, err
		}

		p = grpcplugin

	default:
		cmdplugin, err := createCmdPlugin(args)
		if err != nil {
			return nil, err
		}

		go func() {
			quit <- <-cmdplugin.Quit
		}()

		p = &cmdplugin.GrpcPlugin
	}

	go func() {
		if err := p.RecvLogs(log.StandardLogger().Out); err != nil {
			log.Errorf("plugin %v recv logs error: %v", p.Name, err)
		}
	}()

	return p, nil
}

// setPluginsArgs sets the pending plugins to be started (with the given args).
func (d *daemon) setPluginsArgs(configs [][]string) {
	d.pendingPlugins = make([]*pluginConfig, len(configs))
	for i := range d.pendingPlugins {
		d.pendingPlugins[i] = &pluginConfig{args: configs[i]}
	}
}

func (d *daemon) initializePlugins() error {
	// Start any plugins that haven't started yet.
	for _, rp := range d.pendingPlugins {
		if rp.plugin != nil {
			// already started, skip
			continue
		}
		p, err := startPlugin(rp.args, d.quit)
		if err != nil {
			return fmt.Errorf("failed to start plugin (%q): %v", rp.args, err)
		}
		// mark as started, to prevent future retries
		rp.plugin = p
	}

	// All plugins have started. Install them.
	var plugins []*plugin.GrpcPlugin
	for _, rp := range d.pendingPlugins {
		plugins = append(plugins, rp.plugin)
	}
	if err := d.install(plugins...); err != nil {
		return err
	}

	// Clear pending plugins, so the "fully started and installed?" fast path succeeds.
	d.pendingPlugins = nil
	return nil
}

func (d *daemon) run() error {
	defer d.lis.Close()
	tcpAddr, ok := d.lis.Addr().(*net.TCPAddr)
	port := 0
	if ok {
		port = tcpAddr.Port
	}
	log.WithFields(log.Fields{
		"port": port,
		"addr": d.lis.Addr().String(),
	}).Info("sshpiperd is listening")

	for {
		conn, err := d.lis.Accept()
		if err != nil {
			log.Debugf("failed to accept connection: %v", err)
			continue
		}
		if len(d.pendingPlugins) > 0 {
			err := d.initializePlugins()
			if err != nil {
				log.Errorf("on accept: %v", err)
				conn.Close()
				continue
			}
		}

		log.Debugf("connection accepted: %v", conn.RemoteAddr())

		go func(c net.Conn) {
			defer c.Close()

			// Create a per-connection config copy so we can wrap
			// CreateChallengeContext to inject socket RTT into the
			// metadata before any gRPC calls to the plugin.
			connConfig := d.config.PiperConfig
			if orig := connConfig.CreateChallengeContext; orig != nil {
				connConfig.CreateChallengeContext = func(conn ssh.ServerPreAuthConn) (ssh.ChallengeContext, error) {
					ctx, err := orig(conn)
					if err != nil {
						return ctx, err
					}
					if rtt, rttErr := getSocketRTT(c); rttErr == nil && rtt > 0 {
						if meta, ok := ctx.Meta().(*plugin.PluginConnMeta); ok {
							if meta.Metadata == nil {
								meta.Metadata = make(map[string]string)
							}
							meta.Metadata["socket_rtt_us"] = strconv.FormatInt(rtt.Microseconds(), 10)
						}
					}
					return ctx, nil
				}
			}

			pipec := make(chan *ssh.PiperConn)
			errorc := make(chan error)

			go func() {
				p, err := ssh.NewSSHPiperConn(c, &connConfig)
				if err != nil {
					errorc <- err
					return
				}

				pipec <- p
			}()

			var p *ssh.PiperConn

			select {
			case p = <-pipec:
			case err := <-errorc:
				log.Debugf("connection from %v establishing failed reason: %v", c.RemoteAddr(), err)
				if d.config.PipeCreateErrorCallback != nil {
					d.config.PipeCreateErrorCallback(c, err)
				}

				return
			case <-time.After(d.loginGraceTime):
				log.Debugf("pipe establishing timeout, disconnected connection from %v", c.RemoteAddr())
				if d.config.PipeCreateErrorCallback != nil {
					d.config.PipeCreateErrorCallback(c, fmt.Errorf("pipe establishing timeout"))
				}

				return
			}

			defer p.Close()

			log.Infof("ssh connection pipe created %v (username [%v]) -> %v (username [%v])", p.DownstreamConnMeta().RemoteAddr(), p.DownstreamConnMeta().User(), p.UpstreamConnMeta().RemoteAddr(), p.UpstreamConnMeta().User())

			uphookchain := &hookChain{}
			downhookchain := &hookChain{}

			if d.recorddir != "" {
				var recorddir string
				if d.usernameAsRecorddir {
					recorddir = path.Join(d.recorddir, p.DownstreamConnMeta().User())
				} else {
					uniqID := plugin.GetUniqueID(p.ChallengeContext())
					recorddir = path.Join(d.recorddir, uniqID)
				}
				err = os.MkdirAll(recorddir, 0o700)
				if err != nil {
					log.Errorf("cannot create screen recording dir %v: %v", recorddir, err)
					return
				}

				switch d.recordfmt {
				case "asciicast":
					prefix := ""
					if d.usernameAsRecorddir {
						// add prefix to avoid conflict
						prefix = fmt.Sprintf("%d-", time.Now().Unix())
					}
					recorder := newAsciicastLogger(recorddir, prefix)
					defer recorder.Close()

					uphookchain.append(ssh.InspectPacketHook(recorder.uphook))
					downhookchain.append(ssh.InspectPacketHook(recorder.downhook))
				case "typescript":
					recorder, err := newFilePtyLogger(recorddir)
					if err != nil {
						log.Errorf("cannot create screen recording logger: %v", err)
						return
					}
					defer recorder.Close()

					uphookchain.append(ssh.InspectPacketHook(recorder.loggingTty))
				}
			}

			if d.filterHostkeysReqeust {
				uphookchain.append(func(b []byte) (ssh.PipePacketHookMethod, []byte, error) {
					if b[0] == 80 {
						var x struct {
							RequestName string `sshtype:"80"`
						}
						_ = ssh.Unmarshal(b, &x)
						if x.RequestName == "hostkeys-prove-00@openssh.com" || x.RequestName == "hostkeys-00@openssh.com" {
							return ssh.PipePacketHookTransform, nil, nil
						}
					}

					return ssh.PipePacketHookTransform, b, nil
				})
			}

			if d.replyPing {
				downhookchain.append(ssh.PingPacketReply)
			}

			if d.config.PipeStartCallback != nil {
				d.config.PipeStartCallback(p.DownstreamConnMeta(), p.ChallengeContext())
			}

			err = p.WaitWithHook(uphookchain.hook(), downhookchain.hook())

			if d.config.PipeErrorCallback != nil {
				d.config.PipeErrorCallback(p.DownstreamConnMeta(), p.ChallengeContext(), err)
			}

			log.Infof("connection from %v closed reason: %v", c.RemoteAddr(), err)
		}(conn)
	}
}
