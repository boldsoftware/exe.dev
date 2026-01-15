The [Armored SSH Signature format](https://www.ietf.org/archive/id/draft-josefsson-sshsig-format-03.html) is rich and robust. It contains the public key, the namespace, and more.

It is also long.

In some circumstances, you have out of band information and length matters.

sshminisig is a format to encode _the bare minimum needed for the signature_, assuming all other information, including framing, is done by an outer protocol.

The format is the concatenation of:

- one byte prefix indicating the combination of signature algorithm and hash algorithm
- the signature, base64url-encoded, without padding

Prefixes (shown as ASCII byte):

```
| Prefix | Sig Algorithm                      | Hash Algorithm | Notes                                               |
|--------|------------------------------------|----------------|-----------------------------------------------------|
| e      | ssh-ed25519                        | sha512         | Ed25519 internally uses SHA-512, so this is typical |
| r      | rsa-sha2-256                       | sha256         | Modern RSA variant                                  |
| s      | rsa-sha2-512                       | sha512         | Modern RSA variant                                  |
| c      | ecdsa-sha2-nistp256                | sha512         | NIST P-256 curve                                    |
| d      | ecdsa-sha2-nistp384                | sha512         | NIST P-384 curve                                    |
| p      | ecdsa-sha2-nistp521                | sha512         | NIST P-521 curve                                    |
| f      | sk-ssh-ed25519@openssh.com         | sha512         | Hardware security key (FIDO2)                       |
| g      | sk-ecdsa-sha2-nistp256@openssh.com | sha256         | Hardware security key (FIDO2)                       |
| 2      | ssh-rsa                            | sha256         | Legacy RSA, SHA-1 deprecated                        |
| 5      | ssh-rsa                            | sha512         | Legacy RSA, SHA-1 deprecated                        |
| z      | RESERVED                           | RESERVED       | Reserved for forward-compatibility / version bumps  |
```

The package github.com/boldsoftware/exe.dev/sshminisig is a Go package that converts an Armored SSH Signature (such as that outputted by `ssh-keygen -Y sign`) into an sshminisig. It could be implemented more concisely using (say) github.com/hiddeco/sshsig. But it isn't much code, and by implementing it using only the standard library, the hope is that it'll be easier to port to other languages as needed.

The command github.com/boldsoftware/exe.dev/sshminisig/cmd/sshminisig provides a simple stdin-to-stdout converter.
