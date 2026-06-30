//! exe-scroll: a small, scrollback-aware terminal multiplexer in the spirit of
//! dtach/abduco, written in Zig and using Ghostty's `ghostty-vt` terminal
//! emulator for scrollback-on-reattach.
//!
//! The default invocation takes a single argument -- a path for the session
//! socket -- and *attaches* to it if a live session is there, otherwise
//! *creates* it (spawning $SHELL, or a command after `--`). If the socket file
//! is removed while the session is alive, sending the session server SIGUSR1
//! recreates it, so a later `exe-scroll <path>` can reattach.
//!
//! Two processes are involved: a backgrounded *session server* that owns the
//! pty and the scrollback, and one or more *attach clients* that connect to it
//! over the socket. An attach client detaches (leaving the session running) on
//! SIGUSR2.
//!
//! Licensed under the MIT license (see LICENSE).

const std = @import("std");
const builtin = @import("builtin");
const vt = @import("ghostty-vt");

const alloc = std.heap.c_allocator;

// ----------------------------------------------------------------------------
// libc bindings. Almost everything comes straight from `std.c`/`std.posix`, so
// the ABI (termios layout, signal numbers, ioctl encodings, errno values, ...)
// is whatever Zig defines for the *target* platform rather than hardcoded
// Linux constants -- which is what lets this build for Linux, macOS, and the
// BSDs from the same source. The only locally declared functions are the few
// libc entry points std doesn't wrap: forkpty, execvp, signal, and atexit.
// ----------------------------------------------------------------------------
const c = struct {
    const Termios = std.posix.termios;
    const Winsize = std.posix.winsize;

    extern "c" fn forkpty(amaster: *c_int, name: ?[*]u8, termp: ?*const Termios, winp: ?*const Winsize) c_int;
    extern "c" fn execvp(file: [*:0]const u8, argv: [*:null]const ?[*:0]const u8) c_int;
    extern "c" fn signal(sig: c_int, handler: usize) usize;
    extern "c" fn atexit(f: *const fn () callconv(.c) void) c_int;

    const ioctl = std.c.ioctl;
    const tcgetattr = std.c.tcgetattr;
    const tcsetattr = std.c.tcsetattr;

    fn errno() c_int {
        return std.c._errno().*;
    }
    fn setErrno(v: c_int) void {
        std.c._errno().* = v;
    }

    // signal numbers
    const SIGHUP = std.c.SIG.HUP;
    const SIGINT = std.c.SIG.INT;
    const SIGQUIT = std.c.SIG.QUIT;
    const SIGUSR1 = std.c.SIG.USR1;
    const SIGUSR2 = std.c.SIG.USR2;
    const SIGPIPE = std.c.SIG.PIPE;
    const SIGTERM = std.c.SIG.TERM;
    const SIGWINCH = std.c.SIG.WINCH;
    const SIG_IGN: usize = @intFromPtr(std.c.SIG.IGN);

    // fcntl
    const F_GETFL = std.c.F.GETFL;
    const F_SETFL = std.c.F.SETFL;
    const F_SETFD = std.c.F.SETFD;
    // Passed as a variadic arg to fcntl(), so it needs a fixed-size type.
    const FD_CLOEXEC: c_int = std.c.FD_CLOEXEC;
    const O_NONBLOCK: c_int = @bitCast(@as(u32, @bitCast(std.c.O{ .NONBLOCK = true })));

    // poll
    const POLLIN = std.c.POLL.IN;
    const POLLOUT = std.c.POLL.OUT;
    const POLLERR = std.c.POLL.ERR;
    const POLLHUP = std.c.POLL.HUP;
    const POLLNVAL = std.c.POLL.NVAL;

    // socket
    const AF_UNIX = std.c.AF.UNIX;
    const SOCK_STREAM = std.c.SOCK.STREAM;

    // ioctls. The request is a plain c_int; the platform value may set the high
    // bit, so we bit-cast through u32. The stdlib value's int type differs by
    // platform (comptime_int on Linux, usize on Darwin), hence the @intCast.
    const TIOCGWINSZ: c_int = @bitCast(@as(u32, @intCast(std.c.T.IOCGWINSZ)));
    // std.c.T only defines IOCSWINSZ for some platforms (notably it's missing
    // on Darwin), so fill that gap: it's _IOW('t', 103, struct winsize).
    const TIOCSWINSZ: c_int = switch (builtin.os.tag) {
        .macos, .ios, .tvos, .watchos, .visionos => @bitCast(@as(u32, 0x80087467)),
        else => @bitCast(@as(u32, @intCast(std.c.T.IOCSWINSZ))),
    };

    // tcsetattr action
    const TCSADRAIN = std.c.TCSA.DRAIN;

    // errno values
    const EINTR = @intFromEnum(std.c.E.INTR);
    const EAGAIN = @intFromEnum(std.c.E.AGAIN);
    // EWOULDBLOCK == EAGAIN on Linux (no distinct enum member); keep both names.
    const EWOULDBLOCK = EAGAIN;
    const ENOENT = @intFromEnum(std.c.E.NOENT);
    const EEXIST = @intFromEnum(std.c.E.EXIST);
    const ECONNREFUSED = @intFromEnum(std.c.E.CONNREFUSED);
    const ENAMETOOLONG = @intFromEnum(std.c.E.NAMETOOLONG);
};

// ----------------------------------------------------------------------------
// Build metadata.
// ----------------------------------------------------------------------------
const VERSION = "0.1.0";
const BUGREPORT = "support@exe.dev";

const BUFSIZE = 4096;
// Move the cursor to the bottom so status lines print sanely.
const EOS = "\x1b[999H";
// Cap on a single client output queue before we give up on a stuck client.
const MAX_OUTQ: usize = 32 * 1024 * 1024;
// Cap on a single inbound frame payload (defends the server from a bad client).
const MAX_FRAME: u32 = 4 * 1024 * 1024;
// ----------------------------------------------------------------------------
// Wire framing (server <-> client). A frame is:
//
//   [type:u8][len:u32 little-endian][payload: len bytes]
//
// The format is deliberately simple and easy to extend: add a new type and old
// peers ignore unknown types.
// ----------------------------------------------------------------------------
const MSG_DATA = 1; // both directions: terminal bytes
const MSG_WINCH = 2; // client->server: payload = Winsize (8 bytes)
const MSG_ATTACH = 3; // client->server: payload = [replay:u8]
const MSG_DETACH = 4; // client->server: no payload

const REPLAY_NONE = 0;
const REPLAY_SCREEN = 1;
const REPLAY_SCROLLBACK = 2;

const HDR = 5;

fn putHeader(buf: *[HDR]u8, typ: u8, len: u32) void {
    buf[0] = typ;
    std.mem.writeInt(u32, buf[1..5], len, .little);
}

// ----------------------------------------------------------------------------
// Globals.
// ----------------------------------------------------------------------------
var progname: [:0]const u8 = "exe-scroll";
var sockname: [:0]const u8 = "";
var replay_mode: i32 = REPLAY_SCROLLBACK;
var orig_term: c.Termios = undefined;
var have_tty: bool = false;

var child_argv: [:null]?[*:0]const u8 = undefined;

// Rewrite the process's command line (what `ps` shows) to advertise this
// process's role, e.g. "exe-scroll: session /tmp/work.sock". The portable way
// to do this is to overwrite the contiguous argv string region in place and
// NUL-pad the remainder; `ps` reads the title from there on Linux, macOS, and
// the BSDs. `progname` must already be a copy that does not point into argv
// (see main), since this clobbers argv[0]'s bytes.
fn setProcTitle(role: []const u8) void {
    const argv = std.os.argv;
    if (argv.len == 0) return;
    // The strings argv[i] points at are laid out contiguously; find the span.
    var lo: usize = @intFromPtr(argv[0]);
    var hi: usize = lo;
    for (argv) |a| {
        const s = @intFromPtr(a);
        if (s < lo) lo = s;
        const e = s + std.mem.len(a) + 1; // include the NUL
        if (e > hi) hi = e;
    }
    const cap = hi - lo;
    if (cap < 2) return;
    var buf: [256]u8 = undefined;
    const title = std.fmt.bufPrint(&buf, "exe-scroll: {s} {s}", .{ role, sockname }) catch buf[0..0];
    const dst: [*]u8 = @ptrFromInt(lo);
    const n = @min(title.len, cap - 1);
    @memcpy(dst[0..n], title[0..n]);
    @memset(dst[n..cap], 0);
}

// Scrollback the session retains, as the byte budget ghostty-vt's
// max_scrollback expects.
const SCROLLBACK_BYTES: usize = 1024 * 1024;

// ----------------------------------------------------------------------------
// Small libc helpers.
// ----------------------------------------------------------------------------
fn writeAllFd(fd: c_int, buf: []const u8) void {
    var off: usize = 0;
    while (off < buf.len) {
        const n = std.c.write(fd, buf.ptr + off, buf.len - off);
        if (n > 0) {
            off += @intCast(n);
            continue;
        }
        if (n < 0 and c.errno() == c.EINTR) continue;
        break;
    }
}

fn printErr(comptime fmt: []const u8, args: anytype) void {
    var buf: [1024]u8 = undefined;
    const s = std.fmt.bufPrint(&buf, fmt, args) catch return;
    writeAllFd(2, s);
}

fn setnonblocking(fd: c_int) c_int {
    const flags = std.c.fcntl(fd, c.F_GETFL);
    if (flags < 0) return -1;
    if (std.c.fcntl(fd, c.F_SETFL, flags | c.O_NONBLOCK) < 0) return -1;
    return 0;
}

fn dirOf(path: []const u8) []const u8 {
    if (std.mem.lastIndexOfScalar(u8, path, '/')) |i| {
        if (i == 0) return "/";
        return path[0..i];
    }
    return ".";
}

// ----------------------------------------------------------------------------
// Secure directory creation: mkdir -p with mode 0700 for any component we
// create, so the socket's parents are never world/group accessible.
// ----------------------------------------------------------------------------
fn ensureParentDirs(path: []const u8) !void {
    const dir = dirOf(path);
    if (dir.len == 0 or std.mem.eql(u8, dir, ".") or std.mem.eql(u8, dir, "/")) return;

    var buf: [std.fs.max_path_bytes]u8 = undefined;
    if (dir.len >= buf.len) return error.NameTooLong;
    @memcpy(buf[0..dir.len], dir);
    buf[dir.len] = 0;

    // Walk each prefix and mkdir(0700), ignoring EEXIST.
    var i: usize = 1; // skip a leading '/'
    while (i <= dir.len) : (i += 1) {
        if (i == dir.len or buf[i] == '/') {
            const saved = buf[i];
            buf[i] = 0;
            if (std.c.mkdir(@ptrCast(&buf), 0o700) < 0) {
                const e = c.errno();
                if (e != c.EEXIST) {
                    buf[i] = saved;
                    return error.MkdirFailed;
                }
            }
            buf[i] = saved;
        }
    }
}

// ----------------------------------------------------------------------------
// Ghostty mirror. The session server feeds every byte of program output into an
// in-process Ghostty terminal emulator. On attach we serialize that emulator
// back to VT sequences so a reconnecting client repaints exactly, optionally
// with scrollback.
// ----------------------------------------------------------------------------
const Mirror = struct {
    term: *vt.Terminal,
    stream: vt.TerminalStream,
    cols: u16,
    rows: u16,

    var instance: ?Mirror = null;

    fn init(cols: u16, rows: u16) void {
        const t = alloc.create(vt.Terminal) catch return;
        t.* = vt.Terminal.init(alloc, .{
            .cols = if (cols == 0) 80 else cols,
            .rows = if (rows == 0) 24 else rows,
            .max_scrollback = SCROLLBACK_BYTES,
        }) catch {
            alloc.destroy(t);
            return;
        };
        instance = .{
            .term = t,
            .stream = vt.TerminalStream.initAlloc(alloc, t.vtHandler()),
            .cols = if (cols == 0) 80 else cols,
            .rows = if (rows == 0) 24 else rows,
        };
    }

    fn write(buf: []const u8) void {
        const m = &(instance orelse return);
        m.stream.nextSlice(buf);
    }

    fn resize(cols: u16, rows: u16) void {
        const m = &(instance orelse return);
        if (cols == 0 or rows == 0) return;
        m.term.resize(alloc, cols, rows) catch return;
        m.cols = cols;
        m.rows = rows;
    }

    fn formatTerminal(t: *const vt.Terminal) ?[]u8 {
        var fmt = vt.formatter.TerminalFormatter.init(t, .{ .emit = .vt, .unwrap = false, .trim = true });
        fmt.extra.screen.cursor = true;
        fmt.extra.screen.style = true;
        var aw = std.Io.Writer.Allocating.init(alloc);
        defer aw.deinit();
        fmt.format(&aw.writer) catch return null;
        return aw.toOwnedSlice() catch null;
    }

    /// Build a VT replay snapshot. With `history` we include scrollback;
    /// otherwise we replay the full state through a throwaway scrollback-free
    /// terminal so everything but the visible screen scrolls off. Caller frees.
    fn serialize(history: bool) ?[]u8 {
        const m = &(instance orelse return null);
        if (history) return formatTerminal(m.term);

        const full = formatTerminal(m.term) orelse return null;
        defer alloc.free(full);

        var tmp = vt.Terminal.init(alloc, .{
            .cols = m.cols,
            .rows = m.rows,
            .max_scrollback = 0,
        }) catch return null;
        defer tmp.deinit(alloc);

        var tmp_stream = vt.TerminalStream.initAlloc(alloc, tmp.vtHandler());
        defer tmp_stream.deinit();
        tmp_stream.nextSlice(full);
        return formatTerminal(&tmp);
    }
};

// ----------------------------------------------------------------------------
// Session server process. It owns the pty and the scrollback mirror, and
// relays bytes to and from attach clients over the socket.
// ----------------------------------------------------------------------------
const Pty = struct {
    fd: c_int = -1,
    pid: c_int = -1,
    ws: c.Winsize = .{ .row = 24, .col = 80, .xpixel = 0, .ypixel = 0 },
};
var the_pty: Pty = .{};

const Client = struct {
    next: ?*Client = null,
    fd: c_int,
    attached: bool = false,
    rbuf: std.ArrayListUnmanaged(u8) = .empty, // inbound reassembly
    outq: std.ArrayListUnmanaged(u8) = .empty, // outbound queue
    saved_revents: c_short = 0, // poll revents snapshot for this iteration
};
var clients: ?*Client = null;

// Socket bookkeeping. On exit we only unlink the socket if it's still the one
// we created (its inode matches), so we don't clobber a replacement.
var listen_fd: c_int = -1;
var sock_inode: u64 = 0;
var sock_inode_valid: bool = false;

// Set by the SIGUSR1 handler to ask the session server to recreate its socket
// if it's gone (the abduco approach: send the server SIGUSR1 to rebind a
// missing socket). Read in the serve loop. Accessed atomically since it's
// written from a signal handler.
var recreate_requested = std.atomic.Value(bool).init(false);

// Self-pipe: signal handlers write a byte here so a blocking poll() always
// wakes promptly, closing the race where a signal arrives just before poll()
// blocks. Both the server and attach loops put the read end in their poll set,
// drain it, then act on the atomic flags. Set up before handlers are installed.
var sig_pipe: [2]c_int = .{ -1, -1 };

fn setupSigPipe() void {
    if (std.c.pipe(&sig_pipe) != 0) return;
    _ = setnonblocking(sig_pipe[0]);
    _ = setnonblocking(sig_pipe[1]);
    _ = std.c.fcntl(sig_pipe[0], c.F_SETFD, c.FD_CLOEXEC);
    _ = std.c.fcntl(sig_pipe[1], c.F_SETFD, c.FD_CLOEXEC);
}

/// Async-signal-safe wakeup: a single nonblocking write of one byte.
fn notifySigPipe() void {
    if (sig_pipe[1] >= 0) {
        const b = [1]u8{0};
        _ = std.c.write(sig_pipe[1], &b, 1);
    }
}

fn drainSigPipe() void {
    if (sig_pipe[0] < 0) return;
    var buf: [64]u8 = undefined;
    while (std.c.read(sig_pipe[0], &buf, buf.len) > 0) {}
}

fn serverDie(sig: c_int) callconv(.c) void {
    _ = sig;
    // Tear down the child's process group before we go (kill is async-signal-
    // safe). atexit then unlinks our socket.
    if (the_pty.pid > 0) killPty(c.SIGHUP);
    std.c.exit(1);
}

fn unlinkSocketAtexit() callconv(.c) void {
    // Only remove the socket if it is still the one we created (don't clobber a
    // replacement that a newer server may have put in our place).
    if (currentSocketInode()) |ino| {
        if (sock_inode_valid and ino == sock_inode) {
            _ = std.c.unlink(sockname.ptr);
        }
    }
}

fn currentSocketInode() ?u64 {
    const st = std.fs.cwd().statFile(sockname) catch return null;
    return st.inode;
}

fn initPty(statusfd: c_int) c_int {
    the_pty.pid = c.forkpty(&the_pty.fd, null, null, &the_pty.ws);
    if (the_pty.pid < 0) return -1;
    if (the_pty.pid == 0) {
        // Child: exec the program.
        _ = c.execvp(child_argv[0].?, child_argv.ptr);
        if (statusfd != -1) _ = std.c.dup2(statusfd, 2);
        printErr("{s}: could not execute {s}: errno {d}\r\n", .{ progname, std.mem.span(child_argv[0].?), c.errno() });
        std.c._exit(127);
    }
    return 0;
}

/// Create a fresh listening socket for `name` and atomically move it into
/// place. We bind a temp name in the same directory (so the socket file is
/// never momentarily world-accessible: it is created 0600 before becoming
/// visible) and rename() it over `name`. Returns the fd, or -1.
fn createListenSocket(name: [:0]const u8) c_int {
    var sockun: std.c.sockaddr.un = undefined;

    // temp path: "<dir>/.exe-scroll.<pid>.tmp" in the same directory as name.
    var tmpbuf: [std.fs.max_path_bytes]u8 = undefined;
    const dir = dirOf(name);
    const tmp = std.fmt.bufPrintZ(&tmpbuf, "{s}/.exe-scroll.{d}.tmp", .{
        if (std.mem.eql(u8, dir, ".")) "." else dir,
        std.c.getpid(),
    }) catch {
        c.setErrno(c.ENAMETOOLONG);
        return -1;
    };
    if (tmp.len > sockun.path.len - 1 or name.len > sockun.path.len - 1) {
        c.setErrno(c.ENAMETOOLONG);
        return -1;
    }
    _ = std.c.unlink(tmp.ptr);

    const s = std.c.socket(c.AF_UNIX, c.SOCK_STREAM, 0);
    if (s < 0) return -1;
    sockun.family = c.AF_UNIX;
    @memcpy(sockun.path[0..tmp.len], tmp[0..tmp.len]);
    sockun.path[tmp.len] = 0;
    // Tighten umask so the socket is created with no group/other access even
    // for the instant before the explicit chmod below (defends a socket placed
    // in a pre-existing world-accessible directory like /tmp).
    const old_umask = std.c.umask(0o177);
    const bind_rc = std.c.bind(s, @ptrCast(&sockun), @sizeOf(@TypeOf(sockun)));
    _ = std.c.umask(old_umask);
    if (bind_rc < 0 or
        std.c.chmod(tmp.ptr, 0o600) < 0 or
        std.c.listen(s, 128) < 0 or
        setnonblocking(s) < 0 or
        std.c.rename(tmp.ptr, name.ptr) < 0)
    {
        const e = c.errno();
        _ = std.c.close(s);
        _ = std.c.unlink(tmp.ptr);
        c.setErrno(e);
        return -1;
    }
    _ = std.c.fcntl(s, c.F_SETFD, c.FD_CLOEXEC);
    return s;
}

/// If our socket file is gone (or has been replaced), bind a fresh one so a
/// future client can reattach. Triggered by SIGUSR1 to the session server.
/// Idempotent and cheap when the socket is unchanged (a single stat).
fn maybeRecreateSocket() void {
    const ino = currentSocketInode();
    if (ino != null and sock_inode_valid and ino.? == sock_inode) return; // unchanged

    const fresh = createListenSocket(sockname);
    if (fresh < 0) return; // best effort; a later signal can retry
    _ = std.c.close(listen_fd);
    listen_fd = fresh;
    if (currentSocketInode()) |n| {
        sock_inode = n;
        sock_inode_valid = true;
    }
}

fn requestRecreate(sig: c_int) callconv(.c) void {
    _ = sig;
    _ = c.signal(c.SIGUSR1, @intFromPtr(&requestRecreate));
    recreate_requested.store(true, .seq_cst);
    notifySigPipe();
}

fn killPty(sig: c_int) void {
    _ = std.c.kill(-the_pty.pid, sig);
}

fn dropClient(p: *Client) void {
    _ = std.c.close(p.fd);
    // unlink from the singly linked list
    if (clients == p) {
        clients = p.next;
    } else {
        var q = clients;
        while (q) |cl| : (q = cl.next) {
            if (cl.next == p) {
                cl.next = p.next;
                break;
            }
        }
    }
    p.rbuf.deinit(alloc);
    p.outq.deinit(alloc);
    alloc.destroy(p);
}

fn enqueue(p: *Client, typ: u8, payload: []const u8) void {
    if (p.outq.items.len + HDR + payload.len > MAX_OUTQ) {
        // Stuck/slow client; disconnect it rather than grow unbounded.
        p.outq.clearRetainingCapacity();
        _ = std.c.shutdown(p.fd, 2);
        p.attached = false;
        return;
    }
    // Reserve up front so a frame is enqueued all-or-nothing: a header without
    // its payload would desync the client's framer.
    p.outq.ensureUnusedCapacity(alloc, HDR + payload.len) catch return;
    var hdr: [HDR]u8 = undefined;
    putHeader(&hdr, typ, @intCast(payload.len));
    p.outq.appendSliceAssumeCapacity(&hdr);
    p.outq.appendSliceAssumeCapacity(payload);
}

/// Returns true if the client was dropped (fatal write error).
fn flushOutq(p: *Client) bool {
    while (p.outq.items.len > 0) {
        const n = std.c.write(p.fd, p.outq.items.ptr, p.outq.items.len);
        if (n > 0) {
            const wrote: usize = @intCast(n);
            std.mem.copyForwards(u8, p.outq.items[0 .. p.outq.items.len - wrote], p.outq.items[wrote..]);
            p.outq.items.len -= wrote;
            continue;
        }
        const e = c.errno();
        if (n < 0 and e == c.EINTR) continue;
        if (n < 0 and (e == c.EAGAIN or e == c.EWOULDBLOCK)) return false;
        dropClient(p);
        return true;
    }
    return false;
}

fn sendSnapshot(p: *Client, mode: i32) void {
    const history = (mode == REPLAY_SCROLLBACK);
    const snap = Mirror.serialize(history) orelse return;
    defer alloc.free(snap);
    enqueue(p, MSG_DATA, "\x1b[H\x1b[2J\x1b[3J");
    enqueue(p, MSG_DATA, snap);
}

/// Process one fully-received frame from a client.
fn handleFrame(p: *Client, typ: u8, payload: []const u8) void {
    switch (typ) {
        MSG_DATA => {
            writeAllFd(the_pty.fd, payload);
        },
        MSG_WINCH => {
            if (payload.len >= @sizeOf(c.Winsize)) {
                @memcpy(@as([*]u8, @ptrCast(&the_pty.ws))[0..@sizeOf(c.Winsize)], payload[0..@sizeOf(c.Winsize)]);
                _ = c.ioctl(the_pty.fd, c.TIOCSWINSZ, &the_pty.ws);
                Mirror.resize(the_pty.ws.col, the_pty.ws.row);
            }
        },
        MSG_ATTACH => {
            const mode: i32 = if (payload.len >= 1) payload[0] else REPLAY_SCROLLBACK;
            if (mode != REPLAY_NONE) sendSnapshot(p, mode);
            p.attached = true;
        },
        MSG_DETACH => p.attached = false,
        else => {}, // unknown: ignore (forward-compatible)
    }
}

/// Returns true if the client was dropped.
fn readClient(p: *Client) bool {
    var buf: [BUFSIZE]u8 = undefined;
    const rn = std.c.read(p.fd, &buf, buf.len);
    if (rn < 0 and (c.errno() == c.EAGAIN or c.errno() == c.EINTR)) return false;
    if (rn <= 0) {
        dropClient(p);
        return true;
    }
    p.rbuf.appendSlice(alloc, buf[0..@intCast(rn)]) catch {
        dropClient(p);
        return true;
    };

    while (p.rbuf.items.len >= HDR) {
        const typ = p.rbuf.items[0];
        const len = std.mem.readInt(u32, p.rbuf.items[1..5], .little);
        if (len > MAX_FRAME) {
            dropClient(p);
            return true;
        }
        if (p.rbuf.items.len < HDR + len) break;
        const payload = p.rbuf.items[HDR .. HDR + len];
        handleFrame(p, typ, payload);
        const consumed = HDR + len;
        std.mem.copyForwards(u8, p.rbuf.items[0 .. p.rbuf.items.len - consumed], p.rbuf.items[consumed..]);
        p.rbuf.items.len -= consumed;
    }
    return false;
}

fn acceptClient() void {
    const fd = std.c.accept(listen_fd, null, null);
    if (fd < 0) return;
    if (setnonblocking(fd) < 0) {
        _ = std.c.close(fd);
        return;
    }
    const p = alloc.create(Client) catch {
        _ = std.c.close(fd);
        return;
    };
    p.* = .{ .fd = fd };
    p.next = clients;
    clients = p;
}

var pollbuf: std.ArrayListUnmanaged(std.c.pollfd) = .empty;

fn serveLoop(statusfd: c_int) void {
    _ = std.c.setsid();
    _ = c.atexit(unlinkSocketAtexit);
    // Self-pipe before handlers, so SIGUSR1 always wakes poll() even if it
    // arrives in the window between the flag check and poll() blocking.
    setupSigPipe();
    _ = c.signal(c.SIGPIPE, c.SIG_IGN);
    _ = c.signal(c.SIGHUP, c.SIG_IGN);
    _ = c.signal(c.SIGTERM, @intFromPtr(&serverDie));
    _ = c.signal(c.SIGINT, @intFromPtr(&serverDie));
    // abduco-style: SIGUSR1 asks us to recreate the socket if it's gone.
    _ = c.signal(c.SIGUSR1, @intFromPtr(&requestRecreate));

    if (initPty(statusfd) < 0) {
        printErr("{s}: could not allocate a pty: errno {d}\n", .{ progname, c.errno() });
        std.c.exit(1);
    }
    Mirror.init(the_pty.ws.col, the_pty.ws.row);
    // The child has been forked+exec'd by now, so rewriting our command line
    // (used only for `ps`) can't disturb the child's argv.
    setProcTitle("session");

    // Detach from the controlling environment: success means the parent's
    // readiness pipe closes (it reads EOF) and we run in the background.
    if (statusfd != -1) _ = std.c.close(statusfd);
    const nullfd = std.c.open("/dev/null", .{ .ACCMODE = .RDWR });
    if (nullfd >= 0) {
        _ = std.c.dup2(nullfd, 0);
        _ = std.c.dup2(nullfd, 1);
        _ = std.c.dup2(nullfd, 2);
        if (nullfd > 2) _ = std.c.close(nullfd);
    }

    while (true) {
        pollbuf.clearRetainingCapacity();
        // Fixed slots: 0 = listen socket, 1 = pty, 2 = signal self-pipe.
        // Clients follow from slot 3.
        pollbuf.append(alloc, .{ .fd = listen_fd, .events = c.POLLIN, .revents = 0 }) catch std.c.exit(1);
        pollbuf.append(alloc, .{ .fd = the_pty.fd, .events = c.POLLIN, .revents = 0 }) catch std.c.exit(1);
        pollbuf.append(alloc, .{ .fd = sig_pipe[0], .events = c.POLLIN, .revents = 0 }) catch std.c.exit(1);

        var p = clients;
        while (p) |cl| : (p = cl.next) {
            var ev: c_short = c.POLLIN;
            if (cl.outq.items.len > 0) ev |= c.POLLOUT;
            pollbuf.append(alloc, .{ .fd = cl.fd, .events = ev, .revents = 0 }) catch std.c.exit(1);
        }

        // No timer: block until something happens. A SIGUSR1 recreate request
        // both sets the flag and pokes the self-pipe (slot 2), so poll() wakes
        // even if the signal lands right before we block.
        const nready = std.c.poll(pollbuf.items.ptr, @intCast(pollbuf.items.len), -1);
        if (nready < 0) {
            const e = c.errno();
            if (e == c.EINTR or e == c.EAGAIN) {
                if (recreate_requested.swap(false, .seq_cst)) maybeRecreateSocket();
                continue;
            }
            std.c.exit(1);
        }

        // Drain the self-pipe and honor a pending recreate request.
        if (pollbuf.items[2].revents & c.POLLIN != 0) drainSigPipe();
        if (recreate_requested.swap(false, .seq_cst)) maybeRecreateSocket();

        const listen_ready = (pollbuf.items[0].revents & c.POLLIN) != 0;
        const pty_revents = pollbuf.items[1].revents;
        const pty_ready = (pty_revents & (c.POLLIN | c.POLLHUP | c.POLLERR | c.POLLNVAL)) != 0;

        // Snapshot each client's revents BEFORE mutating the client list
        // (acceptClient prepends a node, which would desync the positional
        // pollfd<->client mapping). Clients start at slot 3 (after listen, pty,
        // self-pipe).
        var idx: usize = 3;
        p = clients;
        while (p) |cl| : (p = cl.next) {
            cl.saved_revents = pollbuf.items[idx].revents;
            idx += 1;
        }

        if (listen_ready) acceptClient();

        // Service clients using the snapshotted revents. flushOutq and
        // readClient may drop the current client, so capture `next` first and
        // stop touching `cl` once it reports dropped.
        p = clients;
        while (p) |cl| {
            const next = cl.next;
            const re = cl.saved_revents;
            var dropped = false;
            if (re & c.POLLOUT != 0) dropped = flushOutq(cl);
            if (!dropped and (re & (c.POLLIN | c.POLLHUP | c.POLLERR | c.POLLNVAL)) != 0)
                dropped = readClient(cl);
            p = next;
        }

        if (pty_ready) ptyActivity();
    }
}

fn ptyActivity() void {
    var buf: [BUFSIZE]u8 = undefined;
    const rn = std.c.read(the_pty.fd, &buf, buf.len);
    if (rn <= 0) std.c.exit(0); // child exited / pty closed
    const data = buf[0..@intCast(rn)];
    Mirror.write(data);
    var p = clients;
    while (p) |cl| : (p = cl.next) {
        if (cl.attached) enqueue(cl, MSG_DATA, data);
    }
}

/// Fork a detached session server. Returns 0 on success (socket is ready),
/// nonzero on failure (message already printed).
fn spawnServer() i32 {
    listen_fd = createListenSocket(sockname);
    if (listen_fd < 0) {
        printErr("{s}: cannot create socket {s}: errno {d}\n", .{ progname, sockname, c.errno() });
        return 1;
    }
    if (currentSocketInode()) |n| {
        sock_inode = n;
        sock_inode_valid = true;
    }

    var fd: [2]c_int = .{ -1, -1 };
    if (std.c.pipe(&fd) < 0) {
        printErr("{s}: pipe failed\n", .{progname});
        _ = std.c.close(listen_fd);
        listen_fd = -1;
        _ = std.c.unlink(sockname.ptr);
        return 1;
    }
    _ = std.c.fcntl(fd[0], c.F_SETFD, c.FD_CLOEXEC);
    _ = std.c.fcntl(fd[1], c.F_SETFD, c.FD_CLOEXEC);

    const pid = std.c.fork();
    if (pid < 0) {
        printErr("{s}: fork failed\n", .{progname});
        _ = std.c.close(fd[0]);
        _ = std.c.close(fd[1]);
        _ = std.c.close(listen_fd);
        listen_fd = -1;
        _ = std.c.unlink(sockname.ptr);
        return 1;
    }
    if (pid == 0) {
        _ = std.c.close(fd[0]);
        serveLoop(fd[1]);
        std.c._exit(0);
    }

    // Parent: wait for the server to signal readiness. On success the server
    // closes its write end (we read EOF); on failure it writes an error first.
    _ = std.c.close(fd[1]);
    var msg: [1024]u8 = undefined;
    const n = std.c.read(fd[0], &msg, msg.len);
    _ = std.c.close(fd[0]);
    if (n > 0) {
        writeAllFd(2, msg[0..@intCast(n)]);
        _ = std.c.kill(pid, c.SIGTERM);
        return 1;
    }
    // The listen fd belongs to the server now; the parent (about to become an
    // attach client) drops it.
    _ = std.c.close(listen_fd);
    listen_fd = -1;
    return 0;
}

// ----------------------------------------------------------------------------
// Attach (foreground) process.
// ----------------------------------------------------------------------------
var cur_term: c.Termios = undefined;
// Set from the SIGWINCH handler; read/cleared in the attach loop. Accessed
// atomically so it is correct under the C memory model (a signal handler may
// store to it at any point).
var win_changed = std.atomic.Value(bool).init(false);
// Set from the SIGUSR2 handler: the user asked this attach client to detach
// (the session keeps running). Read/cleared in the attach loop.
var detach_requested = std.atomic.Value(bool).init(false);

fn restoreTerm() callconv(.c) void {
    if (have_tty) _ = c.tcsetattr(0, c.TCSADRAIN, &orig_term);
}

fn requestDetach(sig: c_int) callconv(.c) void {
    _ = sig;
    _ = c.signal(c.SIGUSR2, @intFromPtr(&requestDetach));
    detach_requested.store(true, .seq_cst);
    notifySigPipe();
}

fn attachDie(sig: c_int) callconv(.c) void {
    if (sig == c.SIGHUP or sig == c.SIGINT) {
        writeAllFd(1, EOS ++ "\r\n[detached]\r\n");
    } else {
        printErr(EOS ++ "\r\n[got signal {d} - dying]\r\n", .{sig});
    }
    std.c.exit(1);
}

fn winChange(sig: c_int) callconv(.c) void {
    _ = sig;
    _ = c.signal(c.SIGWINCH, @intFromPtr(&winChange));
    win_changed.store(true, .seq_cst);
    notifySigPipe();
}

fn connectSocket(name: [:0]const u8) c_int {
    var sockun: std.c.sockaddr.un = undefined;
    const namelen = name.len;
    if (namelen > sockun.path.len - 1) {
        c.setErrno(c.ENAMETOOLONG);
        return -1;
    }
    const s = std.c.socket(c.AF_UNIX, c.SOCK_STREAM, 0);
    if (s < 0) return -1;
    sockun.family = c.AF_UNIX;
    @memcpy(sockun.path[0..namelen], name[0..namelen]);
    sockun.path[namelen] = 0;
    if (std.c.connect(s, @ptrCast(&sockun), @sizeOf(@TypeOf(sockun))) < 0) {
        const e = c.errno();
        _ = std.c.close(s);
        c.setErrno(e);
        return -1;
    }
    return s;
}

fn sendFrame(s: c_int, typ: u8, payload: []const u8) void {
    var hdr: [HDR]u8 = undefined;
    putHeader(&hdr, typ, @intCast(payload.len));
    writeAllFd(s, &hdr);
    if (payload.len > 0) writeAllFd(s, payload);
}

fn sendWinch(s: c_int) void {
    var ws: c.Winsize = .{ .row = 0, .col = 0, .xpixel = 0, .ypixel = 0 };
    _ = c.ioctl(0, c.TIOCGWINSZ, &ws);
    sendFrame(s, MSG_WINCH, @as([*]const u8, @ptrCast(&ws))[0..@sizeOf(c.Winsize)]);
}

/// The interactive attach loop. Returns the process exit code.
fn attachLoop(s: c_int) i32 {
    setProcTitle("attach");
    cur_term = orig_term;
    _ = c.atexit(restoreTerm);

    // Self-pipe so signals reliably interrupt poll(). Set it up before
    // installing handlers so a handler never sees an uninitialized pipe.
    setupSigPipe();
    _ = c.signal(c.SIGPIPE, c.SIG_IGN);
    _ = c.signal(c.SIGHUP, @intFromPtr(&attachDie));
    _ = c.signal(c.SIGTERM, @intFromPtr(&attachDie));
    _ = c.signal(c.SIGINT, @intFromPtr(&attachDie));
    _ = c.signal(c.SIGQUIT, @intFromPtr(&attachDie));
    _ = c.signal(c.SIGWINCH, @intFromPtr(&winChange));
    // Detach (leave the session running) on SIGUSR2.
    _ = c.signal(c.SIGUSR2, @intFromPtr(&requestDetach));

    // Raw mode (equivalent to cfmakeraw): drop input/output post-processing,
    // echo, canonical mode, and signal generation, and read byte-at-a-time.
    cur_term.iflag.IGNBRK = false;
    cur_term.iflag.BRKINT = false;
    cur_term.iflag.PARMRK = false;
    cur_term.iflag.ISTRIP = false;
    cur_term.iflag.INLCR = false;
    cur_term.iflag.IGNCR = false;
    cur_term.iflag.ICRNL = false;
    cur_term.iflag.IXON = false;
    cur_term.iflag.IXOFF = false;
    cur_term.oflag.OPOST = false;
    cur_term.lflag.ECHO = false;
    cur_term.lflag.ECHONL = false;
    cur_term.lflag.ICANON = false;
    cur_term.lflag.ISIG = false;
    cur_term.lflag.IEXTEN = false;
    cur_term.cflag.PARENB = false;
    cur_term.cflag.CSIZE = .CS8;
    cur_term.cc[@intFromEnum(std.c.V.MIN)] = 1;
    cur_term.cc[@intFromEnum(std.c.V.TIME)] = 0;
    _ = c.tcsetattr(0, c.TCSADRAIN, &cur_term);

    writeAllFd(1, "\x1b[H\x1b[J");

    sendWinch(s);
    var rep: [1]u8 = .{@intCast(replay_mode)};
    sendFrame(s, MSG_ATTACH, &rep);

    var reasm: std.ArrayListUnmanaged(u8) = .empty;
    defer reasm.deinit(alloc);
    var rd: [BUFSIZE]u8 = undefined;

    while (true) {
        var pfds = [_]std.c.pollfd{
            .{ .fd = 0, .events = c.POLLIN, .revents = 0 },
            .{ .fd = s, .events = c.POLLIN, .revents = 0 },
            .{ .fd = sig_pipe[0], .events = c.POLLIN, .revents = 0 },
        };
        const n = std.c.poll(&pfds, if (sig_pipe[0] >= 0) 3 else 2, -1);
        // A signal handler may have set a flag (and poked the self-pipe).
        // Handle detach first, then drain the pipe and apply winch.
        if (detach_requested.load(.seq_cst)) {
            // Tell the server we're going so it stops queueing for us; it also
            // notices our disconnect via EOF, but this is prompt and explicit.
            sendFrame(s, MSG_DETACH, "");
            writeAllFd(1, EOS ++ "\r\n[detached]\r\n");
            return 0;
        }
        if (sig_pipe[0] >= 0 and pfds[2].revents & c.POLLIN != 0) drainSigPipe();
        if (n < 0) {
            if (c.errno() == c.EINTR or c.errno() == c.EAGAIN) {
                if (win_changed.swap(false, .seq_cst)) sendWinch(s);
                continue;
            }
            printErr(EOS ++ "\r\n[poll failed]\r\n", .{});
            return 1;
        }

        if (pfds[1].revents & (c.POLLIN | c.POLLHUP) != 0) {
            const r = std.c.read(s, &rd, rd.len);
            if (r == 0) {
                writeAllFd(1, EOS ++ "\r\n[exe-scroll: session ended]\r\n");
                return 0;
            } else if (r < 0) {
                if (c.errno() == c.EINTR or c.errno() == c.EAGAIN) {} else {
                    writeAllFd(1, EOS ++ "\r\n[read error]\r\n");
                    return 1;
                }
            } else {
                // Reassemble frames; only MSG_DATA is expected server->client.
                reasm.appendSlice(alloc, rd[0..@intCast(r)]) catch return 1;
                drainFrames(&reasm);
            }
        }

        if (pfds[0].revents & c.POLLIN != 0) {
            const r = std.c.read(0, &rd, rd.len);
            if (r <= 0) return 1;
            sendFrame(s, MSG_DATA, rd[0..@intCast(r)]);
        }

        if (win_changed.swap(false, .seq_cst)) sendWinch(s);
    }
}

/// Parse complete frames out of `buf`, writing MSG_DATA payloads to stdout and
/// removing the consumed bytes. Incomplete trailing bytes remain buffered.
fn drainFrames(buf: *std.ArrayListUnmanaged(u8)) void {
    var pos: usize = 0;
    const items = buf.items;
    while (items.len - pos >= HDR) {
        const len = std.mem.readInt(u32, items[pos + 1 .. pos + 5][0..4], .little);
        if (len > MAX_FRAME) {
            // Corrupt/hostile peer: stop parsing rather than buffer unbounded.
            buf.clearRetainingCapacity();
            return;
        }
        if (items.len - pos < HDR + len) break;
        const typ = items[pos];
        const payload = items[pos + HDR .. pos + HDR + len];
        if (typ == MSG_DATA) writeAllFd(1, payload);
        pos += HDR + len;
    }
    if (pos > 0) {
        const leftover = items.len - pos;
        std.mem.copyForwards(u8, buf.items[0..leftover], items[pos..]);
        buf.items.len = leftover;
    }
}

// ----------------------------------------------------------------------------
// Entry point and argument parsing.
// ----------------------------------------------------------------------------
fn usage() noreturn {
    const text =
        "{s} {s} -- attach-or-create terminal sessions with ghostty scrollback\n" ++
        "\n" ++
        "Usage:\n" ++
        "  {s} <socket> [-- command...]\n" ++
        "      Attach to the session at <socket>, or create it (running command,\n" ++
        "      default $SHELL) if no live session is there. Intervening directories\n" ++
        "      are created securely (0700).\n" ++
        "\n" ++
        "      Detach (leaving the session running) by sending the attach client\n" ++
        "      SIGUSR2:  kill -USR2 <pid>\n" ++
        "\n" ++
        "Options:\n" ++
        "  -R <mode>   Replay on attach: none | screen | scrollback (default).\n" ++
        "  --version   Print version and exit.\n" ++
        "  -h, --help  Show this help.\n" ++
        "\nReport bugs to <{s}>.\n";
    var buf: [4096]u8 = undefined;
    const s = std.fmt.bufPrint(&buf, text, .{
        progname, VERSION, progname, BUGREPORT,
    }) catch "";
    writeAllFd(1, s);
    std.c.exit(0);
}

pub fn main() u8 {
    const argv = std.os.argv;
    // Copy argv[0] out of the argv string region: setProcTitle overwrites that
    // region in place, and we still want progname for error messages.
    if (argv.len >= 1) progname = alloc.dupeZ(u8, std.mem.span(argv[0])) catch "exe-scroll";
    const args: [][*:0]u8 = argv[0..];

    var i: usize = 1;
    var cmd_start: ?usize = null;

    // Parse options (which may appear before or after the socket). The socket
    // is the first non-option argument; the command (optional) begins at a
    // `--` separator or at the first non-option token after the socket.
    var sock_idx: ?usize = null;
    while (i < args.len) : (i += 1) {
        const tok = std.mem.span(args[i]);
        if (tok.len >= 1 and tok[0] == '-' and !std.mem.eql(u8, tok, "-")) {
            if (std.mem.eql(u8, tok, "--")) {
                // End of options. Anything after is the command (only valid
                // once we already have a socket).
                cmd_start = i + 1;
                i = args.len;
                break;
            }
            if (std.mem.eql(u8, tok, "-h") or std.mem.eql(u8, tok, "--help")) usage();
            if (std.mem.eql(u8, tok, "--version")) {
                var vb: [256]u8 = undefined;
                const vs = std.fmt.bufPrint(&vb, "{s} {s}\n", .{ progname, VERSION }) catch "";
                writeAllFd(1, vs);
                return 0;
            }
            if (std.mem.eql(u8, tok, "-R")) {
                i += 1;
                if (i >= args.len) {
                    printErr("{s}: -R requires an argument\n", .{progname});
                    return 1;
                }
                const a = std.mem.span(args[i]);
                if (std.mem.eql(u8, a, "none")) {
                    replay_mode = REPLAY_NONE;
                } else if (std.mem.eql(u8, a, "screen")) {
                    replay_mode = REPLAY_SCREEN;
                } else if (std.mem.eql(u8, a, "scrollback")) {
                    replay_mode = REPLAY_SCROLLBACK;
                } else {
                    printErr("{s}: invalid replay mode '{s}'\n", .{ progname, a });
                    return 1;
                }
                continue;
            }
            printErr("{s}: unknown option '{s}'\n", .{ progname, tok });
            printErr("Try '{s} --help'.\n", .{progname});
            return 1;
        }
        // First non-option token is the socket; the next non-option token
        // begins the command.
        if (sock_idx == null) {
            sock_idx = i;
        } else {
            cmd_start = i;
            i = args.len;
            break;
        }
    }

    if (sock_idx == null) {
        printErr("{s}: no socket path was specified.\n", .{progname});
        printErr("Try '{s} --help'.\n", .{progname});
        return 1;
    }
    // Copy the socket path out of the argv string region too: setProcTitle
    // overwrites that region, and the server keeps using sockname (e.g. to
    // recreate the socket on SIGUSR1) for its whole life.
    sockname = alloc.dupeZ(u8, std.mem.span(args[sock_idx.?])) catch {
        printErr("{s}: out of memory\n", .{progname});
        return 1;
    };

    // Determine whether we have a controlling terminal.
    if (c.tcgetattr(0, &orig_term) == 0) {
        have_tty = true;
    } else {
        orig_term = std.mem.zeroes(c.Termios);
        have_tty = false;
    }
    if (!have_tty) {
        printErr("{s}: attaching requires a terminal on stdin.\n", .{progname});
        return 1;
    }

    // Build the child argv (used only if we must create the session).
    {
        const start: usize = cmd_start orelse args.len;
        if (start >= args.len) {
            // Default command: $SHELL or /bin/sh.
            const shell = std.posix.getenv("SHELL") orelse "/bin/sh";
            const buf = alloc.allocSentinel(?[*:0]const u8, 1, null) catch return 1;
            buf[0] = @ptrCast(shell.ptr);
            // ensure NUL-terminated: getenv strings are NUL-terminated.
            child_argv = buf;
        } else {
            const remaining = args.len - start;
            const buf = alloc.allocSentinel(?[*:0]const u8, remaining, null) catch return 1;
            var k: usize = 0;
            while (k < remaining) : (k += 1) buf[k] = args[start + k];
            child_argv = buf;
        }
    }

    // Attach-or-create: connect to an existing session, or create one if the
    // socket isn't there. (If a session server is alive but its socket file
    // was removed, send it SIGUSR1 to recreate the socket -- see the server's
    // signal handler -- before reconnecting.)
    var s = connectSocket(sockname);
    if (s < 0) {
        const e = c.errno();
        if (e != c.ENOENT and e != c.ECONNREFUSED) {
            printErr("{s}: {s}: errno {d}\n", .{ progname, sockname, e });
            return 1;
        }
        ensureParentDirs(sockname) catch {
            printErr("{s}: cannot create directories for {s}\n", .{ progname, sockname });
            return 1;
        };
        if (spawnServer() != 0) return 1;
        s = connectSocket(sockname);
        if (s < 0) {
            printErr("{s}: cannot connect to new session {s}: errno {d}\n", .{ progname, sockname, c.errno() });
            return 1;
        }
    }

    return @intCast(attachLoop(s));
}
