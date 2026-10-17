//! End-to-end tests for exe-scroll, run via `zig build test`.
//!
//! These drive the *real* binary (built by build.zig and handed to us as
//! `build_options.exe_path`) over a pseudo-terminal, exactly as a user would:
//! spawn `exe-scroll <socket>`, type at it, read what comes back, detach and
//! reattach, etc. We deliberately avoid poking at internals -- everything is
//! exercised through the socket/pty the way a real client interacts with it.

const std = @import("std");
const opts = @import("build_options");
const testing = std.testing;
const vt = @import("ghostty-vt");

// ----------------------------------------------------------------------------
// libc bindings for the test harness (PTYs + process control).
// ----------------------------------------------------------------------------
const c = struct {
    const Winsize = extern struct {
        ws_row: u16 = 0,
        ws_col: u16 = 0,
        ws_xpixel: u16 = 0,
        ws_ypixel: u16 = 0,
    };
    extern "c" fn forkpty(amaster: *c_int, name: ?[*]u8, termp: ?*const anyopaque, winp: ?*const Winsize) c_int;
    extern "c" fn execvp(file: [*:0]const u8, argv: [*:null]const ?[*:0]const u8) c_int;
    extern "c" fn kill(pid: c_int, sig: c_int) c_int;
    extern "c" fn waitpid(pid: c_int, status: ?*c_int, options: c_int) c_int;
    extern "c" fn _exit(code: c_int) noreturn;
    extern "c" fn alarm(seconds: c_uint) c_uint;
    // std.c._errno() resolves to the platform's errno location (__errno_location
    // on glibc, __error on Darwin, etc.), so this builds everywhere rather than
    // hardcoding the glibc symbol.
    fn errno() c_int {
        return std.c._errno().*;
    }

    const SIGKILL = 9;
    const SIGTERM = 15;
    const SIGUSR1 = 10;
    const SIGUSR2 = 12;
    const EINTR = 4;
    const EAGAIN = 11;
    const POLLIN: c_short = 0x001;
    const WNOHANG = 1;

    // Set the size of a pty via its master fd (what a terminal emulator does
    // on window resize). The kernel then delivers SIGWINCH to the pty's
    // foreground process group. Same encoding dance as exe-scroll.zig.
    // std.c.T only defines IOCSWINSZ for some platforms (notably it's missing
    // on Darwin), so fill that gap the same way exe-scroll.zig does: it's
    // _IOW('t', 103, struct winsize).
    const TIOCSWINSZ: c_int = switch (@import("builtin").os.tag) {
        .macos, .ios, .tvos, .watchos, .visionos => @bitCast(@as(u32, 0x80087467)),
        else => @bitCast(@as(u32, @intCast(std.c.T.IOCSWINSZ))),
    };
};

const alloc = std.heap.c_allocator;
const io = testing.io;

// Milliseconds on a monotonic clock, for deadlines.
fn nowMs() i64 {
    return std.Io.Clock.awake.now(io).toMilliseconds();
}

fn sleepMs(ms: i64) void {
    io.sleep(.fromMilliseconds(ms), .awake) catch {};
}

// A spawned exe-scroll attach client: the pid of the foreground process and
// the controlling-pty fd we talk to it through.
const Proc = struct {
    pid: c_int,
    fd: c_int,

    /// Kill the process (and reap it) and close the pty.
    fn kill(self: *Proc) void {
        if (self.pid > 0) {
            _ = c.kill(self.pid, c.SIGKILL);
            _ = c.waitpid(self.pid, null, 0);
            self.pid = -1;
        }
        if (self.fd >= 0) {
            _ = std.c.close(self.fd);
            self.fd = -1;
        }
    }

    /// Send a Unix signal to the process.
    fn signal(self: *Proc, sig: c_int) void {
        _ = c.kill(self.pid, sig);
    }

    /// Resize the pty this attach client runs on, as a terminal emulator would
    /// when its window changes. The kernel delivers SIGWINCH to the attach
    /// client, which forwards the new size to the session as MSG_WINCH.
    fn resize(self: *Proc, rows: u16, cols: u16) void {
        const ws = c.Winsize{ .ws_row = rows, .ws_col = cols };
        _ = std.c.ioctl(self.fd, c.TIOCSWINSZ, &ws);
    }

    /// Write bytes to the pty (as if typed at the keyboard).
    fn write(self: *Proc, bytes: []const u8) void {
        var off: usize = 0;
        while (off < bytes.len) {
            const n = std.c.write(self.fd, bytes.ptr + off, bytes.len - off);
            if (n > 0) {
                off += @intCast(n);
            } else if (n < 0 and c.errno() == c.EINTR) {
                continue;
            } else break;
        }
    }

    /// Read whatever the program emits over `ms` milliseconds. Returns owned
    /// bytes (caller frees). Stops early on EOF.
    fn drain(self: *Proc, ms: i32) ![]u8 {
        var buf: std.ArrayListUnmanaged(u8) = .empty;
        errdefer buf.deinit(alloc);
        const deadline = nowMs() + ms;
        while (nowMs() < deadline) {
            var pfd = [_]std.c.pollfd{.{ .fd = self.fd, .events = c.POLLIN, .revents = 0 }};
            const n = std.c.poll(&pfd, 1, 50);
            if (n <= 0) continue;
            var tmp: [4096]u8 = undefined;
            const r = std.c.read(self.fd, &tmp, tmp.len);
            if (r > 0) {
                try buf.appendSlice(alloc, tmp[0..@intCast(r)]);
            } else if (r < 0 and (c.errno() == c.EINTR or c.errno() == c.EAGAIN)) {
                continue;
            } else break; // EOF / error
        }
        return buf.toOwnedSlice(alloc);
    }

    /// Drain until `needle` appears or `ms` elapses. Returns all bytes read.
    fn drainUntil(self: *Proc, needle: []const u8, ms: i32) ![]u8 {
        var buf: std.ArrayListUnmanaged(u8) = .empty;
        errdefer buf.deinit(alloc);
        const deadline = nowMs() + ms;
        while (nowMs() < deadline) {
            var pfd = [_]std.c.pollfd{.{ .fd = self.fd, .events = c.POLLIN, .revents = 0 }};
            const n = std.c.poll(&pfd, 1, 50);
            if (n > 0) {
                var tmp: [4096]u8 = undefined;
                const r = std.c.read(self.fd, &tmp, tmp.len);
                if (r > 0) {
                    try buf.appendSlice(alloc, tmp[0..@intCast(r)]);
                    if (std.mem.indexOf(u8, buf.items, needle) != null) break;
                } else if (r < 0 and (c.errno() == c.EINTR or c.errno() == c.EAGAIN)) {
                    continue;
                } else break;
            }
        }
        return buf.toOwnedSlice(alloc);
    }

    /// Wait up to `ms` for the process to exit, reaping it. Returns true if it
    /// exited in time.
    fn waitExit(self: *Proc, ms: i32) bool {
        if (self.pid <= 0) return true;
        const deadline = nowMs() + ms;
        while (true) {
            const r = c.waitpid(self.pid, null, c.WNOHANG);
            if (r == self.pid) {
                self.pid = -1;
                return true;
            }
            if (nowMs() >= deadline) return false;
            sleepMs(10);
        }
    }
};

/// Spawn `exe-scroll args...` on a fresh pty of the given size.
fn spawn(args: []const []const u8, rows: u16, cols: u16) !Proc {
    // Build a NUL-terminated argv: [exe_path, args..., null].
    var argv: std.ArrayListUnmanaged(?[*:0]const u8) = .empty;
    defer argv.deinit(alloc);
    const exe0 = try alloc.dupeZ(u8, opts.exe_path);
    defer alloc.free(exe0);
    try argv.append(alloc, exe0.ptr);
    var owned: std.ArrayListUnmanaged([:0]u8) = .empty;
    defer {
        for (owned.items) |s| alloc.free(s);
        owned.deinit(alloc);
    }
    for (args) |a| {
        const z = try alloc.dupeZ(u8, a);
        try owned.append(alloc, z);
        try argv.append(alloc, z.ptr);
    }
    try argv.append(alloc, null);

    const ws = c.Winsize{ .ws_row = rows, .ws_col = cols };
    var master: c_int = -1;
    const pid = c.forkpty(&master, null, null, &ws);
    if (pid < 0) return error.ForkptyFailed;
    if (pid == 0) {
        const argv_z: [:null]const ?[*:0]const u8 = argv.items[0 .. argv.items.len - 1 :null];
        _ = c.execvp(exe0.ptr, argv_z.ptr);
        c._exit(127);
    }
    return .{ .pid = pid, .fd = master };
}

// ----------------------------------------------------------------------------
// Filesystem + process discovery helpers.
// ----------------------------------------------------------------------------
var seq: usize = 0;

/// A unique socket path under /tmp for this test. Caller frees.
fn sockPath(name: []const u8) ![:0]u8 {
    seq += 1;
    return std.fmt.allocPrintSentinel(alloc, "/tmp/exe-scroll-zt-{d}-{d}-{s}.sock", .{
        std.c.getpid(), seq, name,
    }, 0);
}

fn fileExists(path: []const u8) bool {
    std.Io.Dir.cwd().access(io, path, .{}) catch return false;
    return true;
}

fn cleanup(path: []const u8) void {
    std.Io.Dir.cwd().deleteFile(io, path) catch {};
}

/// Poll for a path to (dis)appear, up to `ms`. Returns true if the predicate
/// held before the deadline.
fn waitFor(path: []const u8, want_exists: bool, ms: i32) bool {
    const deadline = nowMs() + ms;
    while (nowMs() < deadline) {
        if (fileExists(path) == want_exists) return true;
        sleepMs(10);
    }
    return fileExists(path) == want_exists;
}

/// Find the detached session-server pid for `socket`: the exe-scroll process
/// that rewrote its command line to "exe-scroll: session <socket>" (see
/// setProcTitle in exe-scroll.zig), other than `exclude` (the foreground
/// attach client). Returns 0 if none found.
fn findServer(socket: []const u8, exclude: c_int) !c_int {
    var dir = try std.Io.Dir.openDirAbsolute(io, "/proc", .{ .iterate = true });
    defer dir.close(io);
    var needlebuf: [512]u8 = undefined;
    const needle = try std.fmt.bufPrint(&needlebuf, "exe-scroll: session {s}", .{socket});
    var it = dir.iterate();
    while (try it.next(io)) |entry| {
        const pid = std.fmt.parseInt(c_int, entry.name, 10) catch continue;
        if (pid == exclude) continue;
        var pathbuf: [64]u8 = undefined;
        const cmdpath = std.fmt.bufPrint(&pathbuf, "/proc/{s}/cmdline", .{entry.name}) catch continue;
        const data = readProcFile(cmdpath) catch continue;
        defer alloc.free(data);
        // The retitled command line is NUL-padded; the role + socket live in
        // the first NUL-terminated chunk.
        var parts = std.mem.splitScalar(u8, data, 0);
        const arg0 = parts.next() orelse continue;
        if (std.mem.eql(u8, arg0, needle)) return pid;
    }
    return 0;
}

/// Read a /proc file. These report a size of 0, so read them as a stream
/// (Dir.readFileAlloc trusts the size and would return nothing). Caller frees.
fn readProcFile(path: []const u8) ![]u8 {
    var file = try std.Io.Dir.cwd().openFile(io, path, .{});
    defer file.close(io);
    var reader = file.readerStreaming(io, &.{});
    return reader.interface.allocRemaining(alloc, .limited(64 * 1024));
}

fn contains(haystack: []const u8, needle: []const u8) bool {
    return std.mem.indexOf(u8, haystack, needle) != null;
}

/// Canonical VT fingerprint of terminal state, including both buffers. A
/// reconnect-compatible transcript must produce the same fingerprint whether
/// it is consumed uninterrupted or as snapshot + continuation.
fn terminalFingerprint(t: *const vt.Terminal) ![]u8 {
    var out = std.Io.Writer.Allocating.init(alloc);
    defer out.deinit();

    try out.writer.print("active={s}\n", .{@tagName(t.screens.active_key)});
    const formatter_opts: vt.formatter.Options = .{ .emit = .vt, .unwrap = false, .trim = false };
    var terminal_fmt = vt.formatter.TerminalFormatter.init(t, formatter_opts);
    terminal_fmt.extra = .all;
    try terminal_fmt.format(&out.writer);

    inline for (.{ vt.ScreenSet.Key.primary, vt.ScreenSet.Key.alternate }) |key| {
        try out.writer.print("\nscreen={s}\n", .{@tagName(key)});
        if (t.screens.get(key)) |screen| {
            var screen_fmt = vt.formatter.ScreenFormatter.init(screen, formatter_opts);
            screen_fmt.extra = .all;
            try screen_fmt.format(&out.writer);
        }
    }
    return out.toOwnedSlice();
}

// A bare protocol client: a Unix-socket connection speaking exe-scroll's wire
// framing directly, with no attach client (and no pty) in between. Lets tests
// construct protocol states the real client can't be scripted into -- e.g. a
// connection that sends MSG_WINCH but never MSG_ATTACH (the window the
// built-in client is in between its first WINCH and its ATTACH frame), or an
// attached client that has never advertised a window size.
const RawClient = struct {
    const MSG_DATA = 1;
    const MSG_WINCH = 2;
    const MSG_ATTACH = 3;

    const MSG_COMMAND = 5;
    const MSG_DONE = 6;

    fd: c_int,
    rbuf: std.ArrayListUnmanaged(u8) = .empty, // raw inbound bytes
    data: std.ArrayListUnmanaged(u8) = .empty, // reassembled MSG_DATA payloads
    done: ?[]u8 = null, // the MSG_DONE payload, once one arrives

    fn connect(path: []const u8) !RawClient {
        var sa: std.c.sockaddr.un = undefined;
        if (path.len > sa.path.len - 1) return error.NameTooLong;
        const fd = std.c.socket(std.c.AF.UNIX, std.c.SOCK.STREAM, 0);
        if (fd < 0) return error.SocketFailed;
        sa.family = std.c.AF.UNIX;
        @memcpy(sa.path[0..path.len], path);
        sa.path[path.len] = 0;
        if (std.c.connect(fd, @ptrCast(&sa), @sizeOf(@TypeOf(sa))) < 0) {
            _ = std.c.close(fd);
            return error.ConnectFailed;
        }
        return .{ .fd = fd };
    }

    fn sendFrame(self: *RawClient, typ: u8, payload: []const u8) void {
        var hdr: [5]u8 = undefined;
        hdr[0] = typ;
        std.mem.writeInt(u32, hdr[1..5], @intCast(payload.len), .little);
        var buf: std.ArrayListUnmanaged(u8) = .empty;
        defer buf.deinit(alloc);
        buf.appendSlice(alloc, &hdr) catch return;
        buf.appendSlice(alloc, payload) catch return;
        var off: usize = 0;
        while (off < buf.items.len) {
            const n = std.c.write(self.fd, buf.items.ptr + off, buf.items.len - off);
            if (n > 0) {
                off += @intCast(n);
            } else if (n < 0 and c.errno() == c.EINTR) {
                continue;
            } else break;
        }
    }

    fn sendWinch(self: *RawClient, rows: u16, cols: u16) void {
        const ws = c.Winsize{ .ws_row = rows, .ws_col = cols };
        self.sendFrame(MSG_WINCH, std.mem.asBytes(&ws));
    }

    /// MSG_ATTACH with replay disabled (tests want live output, not history).
    fn attach(self: *RawClient) void {
        self.attachReplay(0); // REPLAY_NONE
    }

    fn attachReplay(self: *RawClient, mode: u8) void {
        self.sendFrame(MSG_ATTACH, &.{mode});
    }

    /// Send terminal input (as if typed).
    fn sendData(self: *RawClient, bytes: []const u8) void {
        self.sendFrame(MSG_DATA, bytes);
    }

    /// Read server frames until `needle` appears in the concatenated MSG_DATA
    /// payloads or `ms` elapses. Returns true if found. Payloads accumulate
    /// across calls, so a needle already received is found immediately.
    fn drainUntil(self: *RawClient, needle: []const u8, ms: i32) bool {
        return self.drainFor(needle, ms);
    }

    /// Read server frames until a MSG_DONE arrives (see `done`) or `ms`
    /// elapses. Returns true if one did.
    fn waitDone(self: *RawClient, ms: i32) bool {
        return self.drainFor(null, ms);
    }

    fn drainFor(self: *RawClient, needle: ?[]const u8, ms: i32) bool {
        const deadline = nowMs() + ms;
        while (true) {
            if (needle) |n| {
                if (std.mem.indexOf(u8, self.data.items, n) != null) return true;
            } else if (self.done != null) return true;
            const left = deadline - nowMs();
            if (left <= 0) return false;
            var pfd = [_]std.c.pollfd{.{ .fd = self.fd, .events = c.POLLIN, .revents = 0 }};
            const n = std.c.poll(&pfd, 1, @intCast(@min(left, 50)));
            if (n <= 0) continue;
            var tmp: [4096]u8 = undefined;
            const r = std.c.read(self.fd, &tmp, tmp.len);
            if (r > 0) {
                self.rbuf.appendSlice(alloc, tmp[0..@intCast(r)]) catch return false;
                // Extract complete MSG_DATA payloads (same framing as the
                // real client's drainFrames).
                var pos: usize = 0;
                while (self.rbuf.items.len - pos >= 5) {
                    const len = std.mem.readInt(u32, self.rbuf.items[pos + 1 ..][0..4], .little);
                    if (self.rbuf.items.len - pos < 5 + len) break;
                    const payload = self.rbuf.items[pos + 5 .. pos + 5 + len];
                    if (self.rbuf.items[pos] == MSG_DATA)
                        self.data.appendSlice(alloc, payload) catch return false;
                    if (self.rbuf.items[pos] == MSG_DONE and self.done == null) {
                        self.done = alloc.dupe(u8, payload) catch return false;
                    }
                    pos += 5 + len;
                }
                if (pos > 0) {
                    const leftover = self.rbuf.items.len - pos;
                    std.mem.copyForwards(u8, self.rbuf.items[0..leftover], self.rbuf.items[pos..]);
                    self.rbuf.items.len = leftover;
                }
            } else if (r < 0 and (c.errno() == c.EINTR or c.errno() == c.EAGAIN)) {
                continue;
            } else return false; // EOF
        }
    }

    /// Forget accumulated MSG_DATA (so later drainUntil calls only match new
    /// output).
    fn clearData(self: *RawClient) void {
        self.data.clearRetainingCapacity();
    }

    fn close(self: *RawClient) void {
        if (self.fd >= 0) {
            _ = std.c.close(self.fd);
            self.fd = -1;
        }
        self.rbuf.deinit(alloc);
        self.data.deinit(alloc);
        if (self.done) |d| alloc.free(d);
    }
};

/// Kill the detached session server for `socket` (if any) and wait for it to
/// go away. The server is daemonized (setsid), so it's not our child and can't
/// be waitpid'd -- we SIGKILL it and poll /proc until it's gone, so tests don't
/// leak server (and shell/cat) processes across runs.
fn killServer(socket: []const u8) void {
    const pid = findServer(socket, 0) catch return;
    if (pid <= 0) return;
    _ = c.kill(pid, c.SIGKILL);
    const deadline = nowMs() + 2000;
    while (nowMs() < deadline) {
        if ((findServer(socket, 0) catch 0) <= 0) return;
        sleepMs(10);
    }
}

// ----------------------------------------------------------------------------
// Tests.
// ----------------------------------------------------------------------------

test "--version names the source" {
    const r = try std.process.run(alloc, io, .{ .argv = &.{ opts.exe_path, "--version" } });
    defer alloc.free(r.stdout);
    defer alloc.free(r.stderr);
    try testing.expectEqualStrings(
        try std.fmt.allocPrint(alloc, "{s} {s}\n", .{ opts.exe_path, opts.version }),
        r.stdout,
    );
    // exed's `exe-scroll install` and the e1e tests want 0.<count>.<n>.
    var parts = std.mem.splitScalar(u8, opts.version, '.');
    var n: usize = 0;
    while (parts.next()) |part| : (n += 1) _ = try std.fmt.parseInt(u64, part, 10);
    try testing.expectEqual(3, n);
}

test "create and interact" {
    const s = try sockPath("create");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "echo CREATED; exec cat" }, 24, 80);
    defer p.kill();

    const out = try p.drainUntil("CREATED", 3000);
    defer alloc.free(out);
    try testing.expect(contains(out, "CREATED"));
    try testing.expect(fileExists(s)); // socket was created

    p.write("PINGABC\r");
    const echo = try p.drainUntil("PINGABC", 2000);
    defer alloc.free(echo);
    try testing.expect(contains(echo, "PINGABC")); // interactive echo
}

// The session server ignores SIGHUP and SIGPIPE, and ignored dispositions
// survive exec. A command that inherited them would outlive its session:
// every size-ownership test below used to leave its `stty size` loop running
// forever. A session's end must take its command with it, however it ends.
const PID_THEN_LOOP = "echo PID=$$; while :; do sleep 0.1; done";

fn commandPid(out: []const u8) !c_int {
    const i = std.mem.indexOf(u8, out, "PID=") orelse return error.NoPid;
    var j = i + 4;
    while (j < out.len and std.ascii.isDigit(out[j])) j += 1;
    return std.fmt.parseInt(c_int, out[i + 4 .. j], 10);
}

fn waitGone(pid: c_int, ms: i64) bool {
    const deadline = nowMs() + ms;
    while (nowMs() < deadline) {
        if (c.kill(pid, 0) != 0) return true;
        sleepMs(10);
    }
    return false;
}

fn expectSessionEndKillsCommand(name: []const u8, sig: c_int) !void {
    const s = try sockPath(name);
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", PID_THEN_LOOP }, 24, 80);
    defer p.kill();
    const out = try p.drainUntil("PID=", 3000);
    defer alloc.free(out);
    const out2 = try p.drain(200); // the rest of the number
    defer alloc.free(out2);
    const all = try std.mem.concat(alloc, u8, &.{ out, out2 });
    defer alloc.free(all);
    const pid = try commandPid(all);
    defer _ = c.kill(pid, c.SIGKILL); // don't leak it if the test fails

    const server = try findServer(s, p.pid);
    try testing.expect(server > 0);
    _ = c.kill(server, sig);
    try testing.expect(waitGone(pid, 3000));
}

test "a killed session takes its command with it" {
    try expectSessionEndKillsCommand("end-kill", c.SIGKILL);
}

test "a terminated session takes its command with it" {
    try expectSessionEndKillsCommand("end-term", c.SIGTERM);
}

test "commands get the default SIGPIPE" {
    const s = try sockPath("sigpipe");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // With SIGPIPE ignored, yes gets EPIPE instead and complains about it.
    var p = try spawn(&.{ s, "--", "sh", "-c", "yes | head -c 2 >/dev/null; echo PIPE-DONE; exec cat" }, 24, 80);
    defer p.kill();
    const out = try p.drainUntil("PIPE-DONE", 3000);
    defer alloc.free(out);
    try testing.expect(contains(out, "PIPE-DONE"));
    try testing.expect(!contains(out, "Broken pipe"));
}

test "secure parent dirs are created 0700" {
    seq += 1;
    const base = try std.fmt.allocPrint(alloc, "/tmp/exe-scroll-zt-{d}-{d}-dirs", .{ std.c.getpid(), seq });
    defer alloc.free(base);
    std.Io.Dir.cwd().deleteTree(io, base) catch {};
    defer std.Io.Dir.cwd().deleteTree(io, base) catch {};

    const s = try std.fmt.allocPrintSentinel(alloc, "{s}/a/b/c/session.sock", .{base}, 0);
    defer alloc.free(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "echo NESTED; exec cat" }, 24, 80);
    defer p.kill();
    const out = try p.drainUntil("NESTED", 3000);
    defer alloc.free(out);
    try testing.expect(contains(out, "NESTED"));

    // Every intervening directory we created must be mode 0700.
    for ([_][]const u8{ "a", "a/b", "a/b/c" }) |sub| {
        const d = try std.fmt.allocPrint(alloc, "{s}/{s}", .{ base, sub });
        defer alloc.free(d);
        const st = try std.Io.Dir.cwd().statFile(io, d, .{});
        try testing.expectEqual(@as(u16, 0o700), @as(u16, @intCast(st.permissions.toMode() & 0o777)));
    }
}

test "detach and reattach survives the session" {
    const s = try sockPath("detach");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "echo FIRSTLINE; exec cat" }, 24, 80);
    const first = try p.drainUntil("FIRSTLINE", 3000);
    defer alloc.free(first);
    try testing.expect(contains(first, "FIRSTLINE"));

    // Leave a marker in the session, then detach via SIGUSR2.
    p.write("MARKER42\r");
    const m = try p.drainUntil("MARKER42", 2000);
    defer alloc.free(m);

    p.signal(c.SIGUSR2);
    const bye = try p.drainUntil("detached", 2000);
    defer alloc.free(bye);
    try testing.expect(contains(bye, "detached"));
    try testing.expect(p.waitExit(2000)); // the attach client exited on detach
    p.kill();

    // The session must still be alive: reattach and see the replayed marker.
    try testing.expect(fileExists(s));
    var p2 = try spawn(&.{s}, 24, 80);
    defer p2.kill();
    const replay = try p2.drainUntil("MARKER42", 3000);
    defer alloc.free(replay);
    try testing.expect(contains(replay, "MARKER42")); // scrollback replayed

    p2.write("STILLALIVE\r");
    const alive = try p2.drainUntil("STILLALIVE", 2000);
    defer alloc.free(alive);
    try testing.expect(contains(alive, "STILLALIVE"));
}

test "replay modes: none vs scrollback" {
    // -R none should NOT replay history; -R scrollback should.
    const s = try sockPath("replay");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "echo UNIQHISTORY; exec cat" }, 24, 80);
    const seen = try p.drainUntil("UNIQHISTORY", 3000);
    defer alloc.free(seen);
    try testing.expect(contains(seen, "UNIQHISTORY"));
    p.signal(c.SIGUSR2);
    const d1 = try p.drainUntil("detached", 2000);
    alloc.free(d1);
    p.kill();

    // Reattach with replay disabled: the old line must not be repainted.
    var pn = try spawn(&.{ s, "-R", "none" }, 24, 80);
    const none_out = try pn.drain(1200);
    defer alloc.free(none_out);
    try testing.expect(!contains(none_out, "UNIQHISTORY"));
    pn.signal(c.SIGUSR2);
    const d2 = try pn.drainUntil("detached", 2000);
    alloc.free(d2);
    pn.kill();

    // Reattach with scrollback: the old line is repainted.
    var ps = try spawn(&.{ s, "-R", "scrollback" }, 24, 80);
    defer ps.kill();
    const sb_out = try ps.drainUntil("UNIQHISTORY", 3000);
    defer alloc.free(sb_out);
    try testing.expect(contains(sb_out, "UNIQHISTORY"));
}

test "replay preserves color (SGR escapes)" {
    const s = try sockPath("color");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // Emit bright-green text, then detach and reattach to force a replay.
    var p = try spawn(&.{ s, "--", "sh", "-c", "printf '\\033[1;32mGREENTEXT\\033[0m\\r\\n'; exec cat" }, 24, 80);
    const seen = try p.drainUntil("GREENTEXT", 3000);
    defer alloc.free(seen);
    try testing.expect(contains(seen, "GREENTEXT"));
    p.signal(c.SIGUSR2);
    const d1 = try p.drainUntil("detached", 2000);
    alloc.free(d1);
    p.kill();

    var p2 = try spawn(&.{ s, "-R", "scrollback" }, 24, 80);
    defer p2.kill();
    const replay = try p2.drainUntil("GREENTEXT", 3000);
    defer alloc.free(replay);
    try testing.expect(contains(replay, "GREENTEXT")); // text survived
    try testing.expect(contains(replay, "\x1b[")); // ...and so did SGR escapes
}

test "replay keeps soft-wrapped lines joined" {
    // A line that only wrapped because of the window width must replay as one
    // line, so a client at a different width re-wraps it instead of breaking
    // it again at the old width ("persi" / "s" / "tent"). The space at the
    // wrap column must survive too, or re-wrapping shifts the text.
    const s = try sockPath("unwrap");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // 20 columns: "AAAAAAAAAAAAAAAAAAA BBBBBBBBBBBBBBBBBBB WRAPEND", with the
    // spaces landing on column 20 of the first two rows.
    const line = "AAAAAAAAAAAAAAAAAAA BBBBBBBBBBBBBBBBBBB WRAPEND";
    var p = try spawn(&.{ s, "--", "sh", "-c", "printf '" ++ line ++ "\\r\\n'; exec cat" }, 10, 20);
    const seen = try p.drainUntil("WRAPEND", 3000);
    defer alloc.free(seen);
    p.signal(c.SIGUSR2);
    const d1 = try p.drainUntil("detached", 2000);
    alloc.free(d1);
    p.kill();

    var p2 = try spawn(&.{ s, "-R", "scrollback" }, 10, 20);
    defer p2.kill();
    const replay = try p2.drainUntil("WRAPEND", 3000);
    defer alloc.free(replay);
    try testing.expect(contains(replay, line));
}

fn expectCursorVisibilityReplay(replay_mode: []const u8, script: []const u8, marker: []const u8, expected: []const u8, opposite: []const u8) !void {
    const s = try sockPath(replay_mode);
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", script }, 24, 80);
    const seen = try p.drainUntil(marker, 3000);
    defer alloc.free(seen);
    p.signal(c.SIGUSR2);
    const detached = try p.drainUntil("detached", 2000);
    alloc.free(detached);
    p.kill();

    var p2 = try spawn(&.{ s, "-R", replay_mode }, 24, 80);
    defer p2.kill();
    const replay = try p2.drainUntil(marker, 3000);
    defer alloc.free(replay);

    const state_at = std.mem.indexOf(u8, replay, expected);
    const marker_at = std.mem.indexOf(u8, replay, marker);
    try testing.expect(state_at != null);
    try testing.expect(marker_at != null);
    try testing.expect(state_at.? < marker_at.?);
    try testing.expect(!contains(replay[state_at.? + expected.len .. marker_at.?], opposite));
}

test "replay preserves cursor visibility in every mode" {
    // Claude Code hides the hardware cursor while it draws its own input line.
    // Losing DECTCEM on reattach exposes the real cursor on Claude's scratch
    // row, so it appears to jump below the text during edits. The visible case
    // matters too: reconnects can reuse an emulator whose old state was hidden.
    for ([_][]const u8{ "screen", "scrollback" }) |replay_mode| {
        try expectCursorVisibilityReplay(
            replay_mode,
            "printf '\\033[?25lHIDDENCURSOR\\r\\n'; exec cat",
            "HIDDENCURSOR",
            "\x1b[?25l",
            "\x1b[?25h",
        );
        try expectCursorVisibilityReplay(
            replay_mode,
            "printf '\\033[?25l\\033[?25hVISIBLECURSOR\\r\\n'; exec cat",
            "VISIBLECURSOR",
            "\x1b[?25h",
            "\x1b[?25l",
        );
    }
}

test "replay preserves terminal modes in every mode" {
    // Full-screen TUI views (vim, Claude Code, and Codex overlays) use the
    // alternate screen and mouse reporting so wheel gestures navigate their
    // application-owned history.
    // Replaying only the painted cells makes the screen look right but leaves
    // a fresh emulator on its primary screen with mouse reporting disabled:
    // the wheel then scrolls an empty local buffer and the TUI never hears it.
    for ([_][]const u8{ "screen", "scrollback" }) |replay_mode| {
        const s = try sockPath(replay_mode);
        defer alloc.free(s);
        defer cleanup(s);
        defer killServer(s);

        var p = try spawn(&.{
            s,
            "--",
            "sh",
            "-c",
            "printf 'PRIMARYMODEMARKER\\033[4;7H\\033[31m\\033[?6h\\033[4;7H\\033[?1048h\\033[>4;2m\\033[?1049h\\033[?6l\\033[H\\033[0m\\033[?1003h\\033[?1000h\\033[?1006h\\033[?2004h\\033[?2026hALTMODEMARKER\\033[3;20r\\033[?6h\\033[2;3H'; exec cat",
        }, 24, 80);
        const seen = try p.drainUntil("ALTMODEMARKER", 3000);
        defer alloc.free(seen);
        p.signal(c.SIGUSR2);
        const detached = try p.drainUntil("detached", 2000);
        alloc.free(detached);
        p.kill();

        var p2 = try spawn(&.{ s, "-R", replay_mode }, 24, 80);
        defer p2.kill();
        const replay_head = try p2.drainUntil("ALTMODEMARKER", 3000);
        defer alloc.free(replay_head);
        const replay_tail = try p2.drain(300);
        defer alloc.free(replay_tail);
        const replay = try alloc.alloc(u8, replay_head.len + replay_tail.len);
        defer alloc.free(replay);
        @memcpy(replay[0..replay_head.len], replay_head);
        @memcpy(replay[replay_head.len..], replay_tail);

        const reset_at = std.mem.indexOf(u8, replay, "\x1bc");
        const primary_at = std.mem.indexOf(u8, replay, "PRIMARYMODEMARKER");
        const alt_at = std.mem.indexOf(u8, replay, "ALTMODEMARKER");
        const enter_alt_at = std.mem.indexOf(u8, replay, "\x1b[?1049h");
        try testing.expect(reset_at != null);
        try testing.expect(primary_at != null);
        try testing.expect(alt_at != null);
        try testing.expect(enter_alt_at != null);
        // Reset any reused-emulator state, reconstruct the saved primary
        // buffer, then enter and paint the active alternate screen. Otherwise
        // a later 1049l would restore blank history, and 1049's inherited
        // primary cursor/style would offset or recolor the alternate content.
        try testing.expect(reset_at.? < primary_at.?);
        try testing.expect(primary_at.? < enter_alt_at.?);
        try testing.expect(enter_alt_at.? < alt_at.?);
        const alt_paint_prefix = replay[enter_alt_at.? + "\x1b[?1049h".len .. alt_at.?];
        try testing.expect(contains(alt_paint_prefix, "\x1b[?6l\x1b[H\x1b[0m"));
        for ([_][]const u8{
            "\x1b[?1049h", // alternate screen
            "\x1b[?1000h", // last-set mouse event mode wins
            "\x1b[?1006h", // SGR mouse encoding
            "\x1b[?2004h", // bracketed paste
        }) |mode_sequence| {
            const mode_at = std.mem.indexOf(u8, replay, mode_sequence);
            try testing.expect(mode_at != null);
            try testing.expect(mode_at.? < alt_at.?);
        }
        try testing.expect(!contains(replay, "\x1b[?1003h"));
        // DEC 1048 is an action (save now), not passive state. Replaying it
        // after RIS would save the wrong cursor; 1049 gets its own correctly
        // reconstructed save immediately before the alternate switch.
        try testing.expect(!contains(replay, "\x1b[?1048h"));
        // Synchronized output is a begin/end frame delimiter, not durable
        // state. Replaying a stranded BEGIN would leave xterm.js blank until
        // its watchdog fired because the snapshot has no matching END.
        try testing.expect(!contains(replay, "\x1b[?2026h"));
        try testing.expect(contains(replay, "\x1b[>4;2m"));

        // Apply the real replay to a deliberately dirty client emulator. This
        // catches byte streams that contain the right markers in the wrong
        // terminal state or at the wrong cursor origin.
        var client = try vt.Terminal.init(io, alloc, .{
            .cols = 80,
            .rows = 24,
            .max_scrollback_bytes = 1024 * 1024,
        });
        defer client.deinit(alloc);
        var client_stream = client.vtStream();
        defer client_stream.deinit();
        client_stream.nextSlice("\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[?2004hSTALECLIENT");
        client_stream.nextSlice(replay);

        try testing.expectEqual(vt.ScreenSet.Key.alternate, client.screens.active_key);
        try testing.expectEqual(@as(@TypeOf(client.flags.mouse_event), .normal), client.flags.mouse_event);
        try testing.expectEqual(@as(@TypeOf(client.flags.mouse_format), .sgr), client.flags.mouse_format);
        try testing.expect(client.modes.get(.bracketed_paste));
        try testing.expect(client.flags.modify_other_keys_2);
        try testing.expect(client.modes.get(.origin));
        try testing.expectEqual(@as(usize, 2), client.scrolling_region.top);
        try testing.expectEqual(@as(usize, 19), client.scrolling_region.bottom);
        try testing.expectEqual(@as(usize, 2), client.screens.active.cursor.x);
        try testing.expectEqual(@as(usize, 3), client.screens.active.cursor.y);

        const alternate = try client.screens.active.dumpStringAlloc(alloc, .{ .screen = .{} });
        defer alloc.free(alternate);
        try testing.expect(std.mem.startsWith(u8, alternate, "ALTMODEMARKER"));
        try testing.expect(!contains(alternate, "STALECLIENT"));

        const primary = client.screens.get(.primary) orelse return error.MissingPrimaryScreen;
        const primary_text = try primary.dumpStringAlloc(alloc, .{ .screen = .{} });
        defer alloc.free(primary_text);
        try testing.expect(contains(primary_text, "PRIMARYMODEMARKER"));

        client_stream.nextSlice("\x1b[?1049l");
        try testing.expectEqual(vt.ScreenSet.Key.primary, client.screens.active_key);
        try testing.expect(client.modes.get(.origin));
        try testing.expectEqual(@as(usize, 6), client.screens.active.cursor.x);
        try testing.expectEqual(@as(usize, 3), client.screens.active.cursor.y);
        const restored = try client.screens.active.dumpStringAlloc(alloc, .{ .screen = .{} });
        defer alloc.free(restored);
        try testing.expect(contains(restored, "PRIMARYMODEMARKER"));
    }
}

test "interrupted terminal sequences remain compatible across reconnect" {
    // Real disconnects can land between any two PTY bytes, including halfway
    // through CSI, OSC, charset, UTF-8, and synchronized-output sequences. Run
    // each prefix through the real server, reconnect, append the suffix, and
    // compare the resulting Ghostty terminal state with uninterrupted output.
    const Case = struct {
        name: []const u8,
        prefix: []const u8,
        suffix: []const u8,
        marker: []const u8,
        replay: []const u8,
        pending_needle: ?[]const u8 = null,
    };
    const cases = [_]Case{
        .{
            .name = "sgr",
            .prefix = "\x1bcBASE\x1b[38;2;12;34",
            .suffix = ";56mCOLOR\x1b[0m DONE-SGR",
            .marker = "DONE-SGR",
            .replay = "scrollback",
        },
        .{
            .name = "csi-control",
            .prefix = "\x1bcBASE\x1b[31\n",
            .suffix = "mCOLOR\x1b[0m DONE-CONTROL",
            .marker = "DONE-CONTROL",
            .replay = "screen",
        },
        .{
            .name = "alternate",
            .prefix = "\x1bcBASE-PRIMARY\x1b[?104",
            .suffix = "9hALT DONE-ALT",
            .marker = "DONE-ALT",
            .replay = "scrollback",
        },
        .{
            .name = "charset",
            .prefix = "\x1bcBASE\x1b(",
            .suffix = "0lqqk\x1b(B DONE-CHARSET",
            .marker = "DONE-CHARSET",
            .replay = "screen",
        },
        .{
            .name = "utf8",
            .prefix = "\x1bcBASE-UTF8:\xe2\x98",
            .suffix = "\x83 DONE-UTF8",
            .marker = "DONE-UTF8",
            .replay = "scrollback",
        },
        .{
            .name = "utf8-rollover",
            .prefix = "\x1bcBASE\xe2\x1b[",
            .suffix = "31mCOLOR\x1b[0m DONE-ROLLOVER",
            .marker = "DONE-ROLLOVER",
            .replay = "screen",
        },
        .{
            .name = "dcs-control",
            .prefix = "\x1bcBASE\x1bPqABC\x01",
            .suffix = "DEF\x1b\\ DONE-DCS",
            .marker = "DONE-DCS",
            .replay = "scrollback",
            .pending_needle = "\x1bPqABC\x01",
        },
        .{
            .name = "hyperlink",
            .prefix = "\x1bcBASE\x1b]8;id=x;https://example",
            .suffix = ".com\x1b\\LINK\x1b]8;;\x1b\\ DONE-LINK",
            .marker = "DONE-LINK",
            .replay = "screen",
        },
        .{
            .name = "sync-output",
            .prefix = "\x1bcBASE\x1b[?202",
            .suffix = "6hSYNC\x1b[?2026l DONE-SYNC",
            .marker = "DONE-SYNC",
            .replay = "scrollback",
        },
    };

    for (cases) |case| {
        const s = try sockPath(case.name);
        defer alloc.free(s);
        defer cleanup(s);
        defer killServer(s);

        var p = try spawn(&.{
            s,
            "--",
            "sh",
            "-c",
            "stty raw -echo; printf '\\033cREADY'; exec cat",
        }, 24, 80);
        defer p.kill();
        const ready = try p.drainUntil("READY", 3000);
        alloc.free(ready);

        p.write(case.prefix);
        const prefix = try p.drainUntil(case.prefix, 3000);
        defer alloc.free(prefix);
        try testing.expect(contains(prefix, case.prefix));
        // Simulate a browser/network loss rather than a cooperative detach.
        // The PTY child and session server remain alive behind the socket.
        p.kill();

        // Wait for replayed content before typing so the attach client has
        // put its local pty into raw mode. Bytes after BASE (including the
        // partial sequence) remain ordered ahead of the live suffix.
        var p2 = try spawn(&.{ s, "-R", case.replay }, 24, 80);
        defer p2.kill();
        const replay_head = try p2.drainUntil("BASE", 3000);
        defer alloc.free(replay_head);
        p2.write(case.suffix);
        const replay_tail = try p2.drainUntil(case.marker, 3000);
        defer alloc.free(replay_tail);
        try testing.expect(contains(replay_tail, case.marker));
        const reconnected_bytes = try alloc.alloc(u8, replay_head.len + replay_tail.len);
        defer alloc.free(reconnected_bytes);
        @memcpy(reconnected_bytes[0..replay_head.len], replay_head);
        @memcpy(reconnected_bytes[replay_head.len..], replay_tail);
        if (case.pending_needle) |needle| try testing.expect(contains(reconnected_bytes, needle));

        var uninterrupted = try vt.Terminal.init(io, alloc, .{
            .cols = 80,
            .rows = 24,
            .max_scrollback_bytes = 1024 * 1024,
        });
        defer uninterrupted.deinit(alloc);
        var uninterrupted_stream = uninterrupted.vtStream();
        defer uninterrupted_stream.deinit();
        uninterrupted_stream.nextSlice(case.prefix);
        uninterrupted_stream.nextSlice(case.suffix);

        var reconnected = try vt.Terminal.init(io, alloc, .{
            .cols = 80,
            .rows = 24,
            .max_scrollback_bytes = 1024 * 1024,
        });
        defer reconnected.deinit(alloc);
        var reconnected_stream = reconnected.vtStream();
        defer reconnected_stream.deinit();
        reconnected_stream.nextSlice(reconnected_bytes);

        const want = try terminalFingerprint(&uninterrupted);
        defer alloc.free(want);
        const got = try terminalFingerprint(&reconnected);
        defer alloc.free(got);
        try testing.expectEqualStrings(want, got);
    }
}

test "oversized interrupted sequence shows a stable snapshot then refreshes" {
    const s = try sockPath("oversized-sequence");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // Exceed Mirror's bounded pending-sequence storage while remaining inside
    // DCS. The current client sees the bytes live, but a new client cannot be
    // safely attached until the sequence terminates.
    var p = try spawn(&.{
        s,
        "--",
        "sh",
        "-c",
        "stty raw -echo; printf '\\033cBASE\\033Pq'; dd if=/dev/zero bs=32768 count=1 2>/dev/null | tr '\\000' A; printf TAILPENDING; dd bs=1 count=1 of=/dev/null 2>/dev/null; printf '\\033\\\\READY-OVERSIZE'; dd bs=1 count=1 of=/dev/null 2>/dev/null; stty size; exec cat",
    }, 24, 80);
    defer p.kill();
    const oversized = try p.drainUntil("TAILPENDING", 10000);
    defer alloc.free(oversized);
    try testing.expect(contains(oversized, "TAILPENDING"));
    var client = try RawClient.connect(s);
    defer client.close();
    client.sendWinch(30, 100);
    client.attachReplay(2); // REPLAY_SCROLLBACK
    // The partial DCS is too large to resume, but reconnect must not hang on
    // a blank screen. Show the stable state and suppress unsafe live suffixes.
    try testing.expect(client.drainUntil("BASE", 3000));

    // The old owner disappears while this attach is waiting for a refresh.
    // Completing the DCS must replace the degraded snapshot while preserving
    // B's recorded 30x100 size ownership.
    p.kill();
    client.sendData("x");
    try testing.expect(client.drainUntil("READY-OVERSIZE", 5000));
    client.sendData("y");
    try testing.expect(client.drainUntil("30 100", 5000));

    var terminal = try vt.Terminal.init(io, alloc, .{
        .cols = 80,
        .rows = 24,
        .max_scrollback_bytes = 1024 * 1024,
    });
    defer terminal.deinit(alloc);
    var stream = terminal.vtStream();
    defer stream.deinit();
    stream.nextSlice(client.data.items);

    const rendered = try terminal.screens.active.dumpStringAlloc(alloc, .{ .screen = .{} });
    defer alloc.free(rendered);
    try testing.expect(contains(rendered, "BASE"));
    try testing.expect(contains(rendered, "30 100"));
    try testing.expect(!contains(rendered, "TAILPENDING"));
}

test "replay preserves an inactive legacy alternate screen" {
    // DEC 47 keeps the alternate buffer when switching back to primary. RIS
    // gives replays a deterministic baseline, so reconstruct an initialized
    // inactive buffer too rather than losing it until the next 47h.
    for ([_][]const u8{ "screen", "scrollback" }) |replay_mode| {
        const s = try sockPath(replay_mode);
        defer alloc.free(s);
        defer cleanup(s);
        defer killServer(s);

        var p = try spawn(&.{
            s,
            "--",
            "sh",
            "-c",
            "printf 'PRIMARY-ACTIVE\\033[5;1HPRIMARY-KEEP\\033[?47h\\033[HALTERNATE-KEEP\\033[?47l\\033[HPRIMARY-ACTIVE'; exec cat",
        }, 24, 80);
        const seen = try p.drainUntil("ALTERNATE-KEEP", 3000);
        defer alloc.free(seen);
        p.signal(c.SIGUSR2);
        const detached = try p.drainUntil("detached", 2000);
        alloc.free(detached);
        p.kill();

        var p2 = try spawn(&.{ s, "-R", replay_mode }, 24, 80);
        defer p2.kill();
        const replay = try p2.drain(1200);
        defer alloc.free(replay);

        var client = try vt.Terminal.init(io, alloc, .{
            .cols = 80,
            .rows = 24,
            .max_scrollback_bytes = 1024 * 1024,
        });
        defer client.deinit(alloc);
        var client_stream = client.vtStream();
        defer client_stream.deinit();
        client_stream.nextSlice(replay);

        try testing.expectEqual(vt.ScreenSet.Key.primary, client.screens.active_key);
        const primary = try client.screens.active.dumpStringAlloc(alloc, .{ .screen = .{} });
        defer alloc.free(primary);
        try testing.expect(contains(primary, "PRIMARY-ACTIVE"));
        try testing.expect(contains(primary, "PRIMARY-KEEP"));

        client_stream.nextSlice("\x1b[?47h");
        try testing.expectEqual(vt.ScreenSet.Key.alternate, client.screens.active_key);
        const alternate = try client.screens.active.dumpStringAlloc(alloc, .{ .screen = .{} });
        defer alloc.free(alternate);
        try testing.expect(contains(alternate, "ALTERNATE-KEEP"));
    }
}

test "socket recreation on SIGUSR1 (abduco-style)" {
    const s = try sockPath("recreate");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "echo ALIVE; exec cat" }, 24, 80);
    defer p.kill();
    const up = try p.drainUntil("ALIVE", 3000);
    defer alloc.free(up);
    try testing.expect(fileExists(s));
    p.write("PERSISTED\r");
    const m = try p.drainUntil("PERSISTED", 2000);
    defer alloc.free(m);

    const server = try findServer(s, p.pid);
    try testing.expect(server > 0); // found the detached session server

    const ino_before = (try std.Io.Dir.cwd().statFile(io, s, .{})).inode;

    // Delete the socket out from under the live session.
    cleanup(s);
    try testing.expect(!fileExists(s));
    // The server must NOT recreate it on its own (no timer/watcher).
    sleepMs(800);
    try testing.expect(!fileExists(s));

    // Signal the server: it should rebind a fresh socket.
    _ = c.kill(server, c.SIGUSR1);
    try testing.expect(waitFor(s, true, 2000));
    const ino_after = (try std.Io.Dir.cwd().statFile(io, s, .{})).inode;
    try testing.expect(ino_after != ino_before); // fresh inode

    // Reattach to the still-running session and confirm continuity.
    var p2 = try spawn(&.{ s, "-R", "scrollback" }, 24, 80);
    defer p2.kill();
    const replay = try p2.drainUntil("PERSISTED", 3000);
    defer alloc.free(replay);
    try testing.expect(contains(replay, "PERSISTED"));
    p2.write("AFTERRECREATE\r");
    const alive = try p2.drainUntil("AFTERRECREATE", 2000);
    defer alloc.free(alive);
    try testing.expect(contains(alive, "AFTERRECREATE"));
}

// ----------------------------------------------------------------------------
// Size-ownership tests. Multiple clients attached to one session used to
// fight over the PTY size (every MSG_WINCH applied; last write wins). The
// rule now is "typing claims the size": a resize from a non-owner is recorded
// but not applied; actually typing takes ownership (and applies your size).
//
// To observe the PTY size WITHOUT typing (typing would itself claim
// ownership!), the session command is a loop that prints `stty size` a few
// times a second; every attached client sees the stream, so a size change --
// or its absence -- is visible in the drained output.
// ----------------------------------------------------------------------------
const SIZE_PRINTER = "while :; do stty size; sleep 0.2; done";

/// Make `p` the size owner by typing, and wait until the pty reports `size`.
/// Ownership tests use this to turn a freshly attached client (which claims
/// the size by attaching) back into a bystander.
fn reclaim(p: *Proc, size: []const u8) !void {
    p.write("x");
    const out = try p.drainUntil(size, 3000);
    defer alloc.free(out);
    try testing.expect(contains(out, size));
}

/// Discard `p`'s buffered output so later checks see only fresh size lines.
fn flush(p: *Proc) !void {
    alloc.free(try p.drain(500));
}

test "size ownership: attaching claims the size" {
    const s = try sockPath("own-attach");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // A (say, a wide desktop browser) creates the session and owns the size.
    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 46, 196);
    defer a.kill();
    const a0 = try a.drainUntil("46 196", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "46 196"));

    // B (a phone) attaches while A is still attached. Opening a terminal is a
    // claim: B must get its own size without typing anything. Otherwise the
    // application keeps drawing for A's 196 columns into B's 50, and B shows
    // garbage until the user happens to type.
    var b = try spawn(&.{s}, 39, 50);
    defer b.kill();
    const b0 = try b.drainUntil("39 50", 3000);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "39 50"));

    // A is now a bystander: its resizes are recorded, not applied...
    try flush(&a);
    a.resize(46, 161);
    const a1 = try a.drain(1000);
    defer alloc.free(a1);
    try testing.expect(contains(a1, "39 50"));
    try testing.expect(!contains(a1, "46 161"));

    // ...until A types, which claims the size back at A's latest size.
    try reclaim(&a, "46 161");
}

test "size ownership: typing claims the size" {
    const s = try sockPath("own-type");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // A creates the session at 24x80 and therefore owns the size.
    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    defer a.kill();
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    // B attaches at 30x100, which claims the size; A types to take it back.
    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    const b_attach = try b.drainUntil("30 100", 3000);
    alloc.free(b_attach);
    try reclaim(&a, "24 80");

    // B resizes to 32x104 while A owns: recorded, not applied.
    try flush(&b);
    b.resize(32, 104);
    const b0 = try b.drain(1000);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "24 80")); // still A's size
    try testing.expect(!contains(b0, "32 104")); // B's WINCH was not applied

    // B types: that claims ownership and applies B's recorded 32x104.
    try reclaim(&b, "32 104");

    // A resizes to 40x120. B owns the size now, so A's WINCH is recorded but
    // not applied.
    try flush(&a);
    a.resize(40, 120);
    const a1 = try a.drain(1000);
    defer alloc.free(a1);
    try testing.expect(contains(a1, "32 104")); // still B's size
    try testing.expect(!contains(a1, "40 120")); // A's WINCH was not applied

    // A types: ownership moves back to A and its recorded 40x120 applies.
    a.write("x");
    const a2 = try a.drainUntil("40 120", 3000);
    defer alloc.free(a2);
    try testing.expect(contains(a2, "40 120"));
}

test "size ownership: owner detach releases the size" {
    const s = try sockPath("own-detach");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // A creates the session; B and C attach at other sizes (each claiming
    // the size), then A types to own it again.
    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    alloc.free(try b.drainUntil("30 100", 3000));
    var cc = try spawn(&.{s}, 35, 110);
    defer cc.kill();
    alloc.free(try cc.drainUntil("35 110", 3000));
    try reclaim(&a, "24 80");

    // The owner detaches: the size becomes unowned. Nobody inherits it
    // automatically -- the next WINCH (or input) claims it.
    a.signal(c.SIGUSR2);
    const bye = try a.drainUntil("detached", 2000);
    defer alloc.free(bye);
    try testing.expect(a.waitExit(2000));
    a.kill();
    sleepMs(200); // let the server process it

    // B's resize is the first WINCH after the release: it applies, and B
    // becomes the owner.
    b.resize(50, 150);
    const b1 = try b.drainUntil("50 150", 3000);
    defer alloc.free(b1);
    try testing.expect(contains(b1, "50 150"));

    // C's resize must now be ignored: B owns the size (and C never typed).
    try flush(&cc);
    cc.resize(60, 160);
    const c0 = try cc.drain(1000);
    defer alloc.free(c0);
    try testing.expect(contains(c0, "50 150"));
    try testing.expect(!contains(c0, "60 160"));
}

test "size ownership: sole client resize always applies" {
    const s = try sockPath("own-sole");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // With a single attached client the behavior is exactly as before this
    // feature existed: every resize applies immediately, no typing needed.
    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    defer a.kill();
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    a.resize(26, 90);
    const a1 = try a.drainUntil("26 90", 3000);
    defer alloc.free(a1);
    try testing.expect(contains(a1, "26 90"));

    a.resize(27, 91);
    const a2 = try a.drainUntil("27 91", 3000);
    defer alloc.free(a2);
    try testing.expect(contains(a2, "27 91"));
}

test "size ownership: terminal auto-replies do not claim" {
    const s = try sockPath("own-autoreply");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // A creates (and owns) the session at 24x80.
    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    defer a.kill();
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    // B attaches at 30x100 (claiming the size); A types to take it back,
    // leaving B a bystander.
    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    alloc.free(try b.drainUntil("30 100", 3000));
    try reclaim(&a, "24 80");
    try flush(&b);

    // Emulator auto-replies riding the input path: focus-in report, a DA1
    // response, and a cursor position report. None of these are typing, so
    // none may claim size ownership.
    b.write("\x1b[I");
    b.write("\x1b[?62c");
    b.write("\x1b[12;40R");
    // A CPR split across two input frames: the classifier state must carry
    // across MSG_DATA payloads for the second half to stay an auto-reply.
    b.write("\x1b[12;");
    sleepMs(150);
    b.write("40R");

    const b0 = try b.drain(1000);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "24 80")); // still A's size
    try testing.expect(!contains(b0, "30 100")); // nothing claimed

    // A genuine keystroke from the same client does claim (and applies its
    // recorded size).
    b.write("x");
    const b1 = try b.drainUntil("30 100", 3000);
    defer alloc.free(b1);
    try testing.expect(contains(b1, "30 100"));
}

test "size ownership: abrupt owner disconnect snaps to the sole survivor" {
    const s = try sockPath("own-drop");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    // B attaches (claiming the size), then A types to own it again.
    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    alloc.free(try b.drainUntil("30 100", 3000));
    try reclaim(&a, "24 80");
    try flush(&b);

    // Kill the owner outright: no MSG_DETACH is ever sent -- the server only
    // sees the connection close (EOF) and must release ownership in the
    // dropClient path. That leaves exactly one attached client (B) with a
    // valid advertised size, so the pty must snap to B's size WITHOUT B
    // sending anything (the browser-reload scenario: the new tab's resize
    // was swallowed while the old connection lingered, and the web client
    // won't re-send a size it believes is current).
    a.kill();
    const b1 = try b.drainUntil("30 100", 3000);
    defer alloc.free(b1);
    try testing.expect(contains(b1, "30 100"));

    // And the survivor (now owner) resizes freely.
    b.resize(50, 150);
    const b2 = try b.drainUntil("50 150", 3000);
    defer alloc.free(b2);
    try testing.expect(contains(b2, "50 150"));
}

test "size ownership: never-attached owner is displaced by a real client" {
    // A connected-but-never-attached client can claim the unowned size with
    // its first WINCH (the attach protocol sends WINCH before MSG_ATTACH,
    // so that's a legitimate transient). But only an attached client may
    // HOLD the size: the next WINCH from a real client treats the stale
    // owner as no owner, applies, and takes ownership -- otherwise a client
    // that dies mid-handshake would own forever and lock everyone out.
    const s = try sockPath("own-stale");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    // A detaches: the size becomes unowned (no other attached client, so no
    // sole-survivor snap either).
    a.signal(c.SIGUSR2);
    const bye = try a.drainUntil("detached", 2000);
    defer alloc.free(bye);
    try testing.expect(a.waitExit(2000));
    a.kill();
    sleepMs(200);

    // A raw protocol client claims the unowned size with a WINCH and never
    // attaches (frozen in the built-in client's WINCH-before-ATTACH window).
    var r = try RawClient.connect(s);
    defer r.close();
    r.sendWinch(50, 150);
    sleepMs(500); // let a "50 150" line print

    // B attaches at 30x100. Its attach-time WINCH displaces the stale
    // (never-attached) owner immediately (and attaching would claim anyway).
    var b = try spawn(&.{ s, "-R", "scrollback" }, 30, 100);
    defer b.kill();
    const b0 = try b.drainUntil("30 100", 3000);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "50 150")); // R's claim did apply (replayed)
    try testing.expect(contains(b0, "30 100")); // ...and B displaced it
}

test "size ownership: degenerate 0x0 winch neither claims nor applies" {
    const s = try sockPath("own-zero");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    // A detaches: unowned, no attached clients left.
    a.signal(c.SIGUSR2);
    const bye = try a.drainUntil("detached", 2000);
    defer alloc.free(bye);
    try testing.expect(a.waitExit(2000));
    a.kill();
    sleepMs(200);

    // R1 attaches and advertises 0x0 (e.g. a hidden window). Before the
    // validity guard this claimed ownership AND set the pty to 0x0 via
    // TIOCSWINSZ -- and with ownership, nothing would overwrite it (wedged).
    var r1 = try RawClient.connect(s);
    defer r1.close();
    r1.attach();
    r1.sendWinch(0, 0);
    try testing.expect(!r1.drainUntil("0 0", 800)); // pty never became 0x0
    try testing.expect(contains(r1.data.items, "24 80")); // still the old size

    // A valid winch from a second attached client applies: R1's 0x0 claimed
    // nothing (if it had, R1 -- attached -- would block R2 here).
    var r2 = try RawClient.connect(s);
    defer r2.close();
    r2.attach();
    r2.sendWinch(40, 120);
    try testing.expect(r2.drainUntil("40 120", 3000));
}

test "size ownership: typing without a size never claims (no deadlock)" {
    const s = try sockPath("own-sizeless");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    defer a.kill();
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    // R attaches and TYPES without ever having sent a MSG_WINCH (reachable in
    // production: the web frontend forwards input regardless of resize
    // ordering, and iOS drops pre-open resizes). A size-less client must not
    // become owner: it can't contribute a size, and as owner it would block
    // everyone else's resizes.
    var r = try RawClient.connect(s);
    defer r.close();
    r.attach();
    r.sendData("x");
    sleepMs(300); // let the server process it

    // A (the owner) can still resize; had R claimed, this would be swallowed
    // (R is attached, so neither the stale-owner nor sole-client rule helps).
    a.resize(26, 90);
    const a1 = try a.drainUntil("26 90", 3000);
    defer alloc.free(a1);
    try testing.expect(contains(a1, "26 90"));
}

test "size ownership: mouse wheel does not claim, click does" {
    const s = try sockPath("own-wheel");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    defer a.kill();
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    alloc.free(try b.drainUntil("30 100", 3000));
    try reclaim(&a, "24 80");
    try flush(&b);

    // SGR mouse wheel reports (what web/iOS turn scroll gestures into):
    // wheel-up press, wheel-down with the release final, shift+wheel-up
    // (modifier bit OR'd in). Idly scrolling must not steal the size.
    b.write("\x1b[<64;12;40M");
    b.write("\x1b[<65;12;40m");
    b.write("\x1b[<68;12;40M");
    const b0 = try b.drain(800);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "24 80")); // still A's size
    try testing.expect(!contains(b0, "30 100")); // wheel claimed nothing

    // A left-button CLICK is deliberate interaction: it claims.
    b.write("\x1b[<0;12;40M");
    const b1 = try b.drainUntil("30 100", 3000);
    defer alloc.free(b1);
    try testing.expect(contains(b1, "30 100"));
}

test "size ownership: unterminated OSC cannot lock a client out" {
    const s = try sockPath("own-osc");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    defer a.kill();
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    alloc.free(try b.drainUntil("30 100", 3000));
    try reclaim(&a, "24 80");
    try flush(&b);

    // An OSC that never terminates: everything after it, keystrokes
    // included, is string body to the scanner -- so this must NOT claim...
    b.write("\x1b]0;stray-title");
    b.write("x");
    const b0 = try b.drain(800);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "24 80"));
    try testing.expect(!contains(b0, "30 100"));

    // ...but not forever: past the 8 KiB string cap the scanner assumes the
    // terminator was lost and returns to ground, so the client can claim
    // again. Pump >8 KiB of body through, then type.
    const junk = try alloc.alloc(u8, 9 * 1024);
    defer alloc.free(junk);
    @memset(junk, 'a');
    b.write(junk);
    b.write("x");
    const b1 = try b.drainUntil("30 100", 5000);
    defer alloc.free(b1);
    try testing.expect(contains(b1, "30 100"));
}

// ----------------------------------------------------------------------------
// Commands: send-keys and capture-pane. They run with no terminal, the way
// scripts and agents use them.
// ----------------------------------------------------------------------------
const Ran = struct {
    code: u8,
    stdout: []u8,
    stderr: []u8,

    fn deinit(self: Ran) void {
        alloc.free(self.stdout);
        alloc.free(self.stderr);
    }
};

/// Run `exe-scroll args...` to completion, without a terminal. A command
/// that takes over 20 seconds is killed and exits 124.
fn command(args: []const []const u8) !Ran {
    var argv: std.ArrayListUnmanaged([]const u8) = .empty;
    defer argv.deinit(alloc);
    try argv.appendSlice(alloc, &.{ "timeout", "20", opts.exe_path });
    try argv.appendSlice(alloc, args);
    const r = try std.process.run(alloc, io, .{ .argv = argv.items });
    const code: u8 = switch (r.term) {
        .exited => |code| code,
        else => 255,
    };
    return .{ .code = code, .stdout = r.stdout, .stderr = r.stderr };
}

/// Run `exe-scroll cmd socket args...`.
fn run(cmd: []const u8, s: []const u8, args: []const []const u8) !Ran {
    var argv: std.ArrayListUnmanaged([]const u8) = .empty;
    defer argv.deinit(alloc);
    try argv.appendSlice(alloc, &.{ cmd, s });
    try argv.appendSlice(alloc, args);
    return command(argv.items);
}

/// capture-pane (with `args`) until the capture contains `needle`, and
/// return it.
fn captureUntil(s: []const u8, args: []const []const u8, needle: []const u8, ms: i64) ![]u8 {
    const deadline = nowMs() + ms;
    while (true) {
        const r = try run("capture-pane", s, args);
        alloc.free(r.stderr);
        if (contains(r.stdout, needle)) return r.stdout;
        alloc.free(r.stdout);
        if (nowMs() >= deadline) return error.Timeout;
        sleepMs(20);
    }
}

fn expectCapture(s: []const u8, args: []const []const u8, want: []const u8) !void {
    const r = try run("capture-pane", s, args);
    defer r.deinit();
    try testing.expectEqualStrings("", r.stderr);
    try testing.expectEqual(0, r.code);
    try testing.expectEqualStrings(want, r.stdout);
}

fn expectCommandFails(args: []const []const u8, msg: []const u8) !void {
    try expectFails(try command(args), msg);
}

fn expectFails(r: Ran, msg: []const u8) !void {
    defer r.deinit();
    try testing.expectEqual(1, r.code);
    if (!contains(r.stderr, msg)) {
        std.debug.print("stderr [{s}] lacks [{s}]\n", .{ r.stderr, msg });
        return error.TestUnexpectedResult;
    }
}

fn sendKeys(s: []const u8, keys: []const []const u8) !void {
    const r = try run("send-keys", s, keys);
    defer r.deinit();
    try testing.expectEqualStrings("", r.stderr);
    try testing.expectEqual(0, r.code);
}

/// Expect the session's next hex dump (see the readers below) to be `hex`.
fn expectBytes(p: *Proc, hex: []const u8) !void {
    const out = try p.drainUntil(" END", 3000);
    defer alloc.free(out);
    const want = try std.fmt.allocPrint(alloc, "{s} END", .{hex});
    defer alloc.free(want);
    if (!contains(out, want)) {
        std.debug.print("got [{s}], want [{s}]\n", .{ out, want });
        return error.TestUnexpectedResult;
    }
}

// Hex-dumps what it reads: `head -c N | od` for each count, in raw mode so
// every byte arrives untranslated.
const HEX = " | od -An -tx1 | tr -d '\\n'; printf ' END\\r\\n'";

test "send-keys presses named keys and types the rest" {
    const s = try sockPath("send-keys");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "stty raw -echo; printf 'READY\\r\\n'; head -c 27" ++ HEX ++ "; head -c 10" ++ HEX ++ "; exec cat" }, 24, 80);
    defer p.kill();
    alloc.free(try p.drainUntil("READY", 3000));

    // tmux's rules: words naming keys (in any case, with C-/M-/S-/^
    // modifiers) are pressed; other words are typed. C-m is Enter.
    try sendKeys(s, &.{ "hi there", "C-m", "up", "C-c", "M-x", "BTab", "Escape", "BSpace", "^b", "S-a", "F5" });
    try expectBytes(&p, " 68 69 20 74 68 65 72 65 0d 1b 5b 41 03 1b 78 1b 5b 5a 1b 7f 02 41 1b 5b 31 35 7e");

    // -l types every word as is; -- ends the options.
    try sendKeys(s, &.{ "-l", "Enter", "C-c" });
    try sendKeys(s, &.{ "--", "-l" });
    try expectBytes(&p, " 45 6e 74 65 72 43 2d 63 2d 6c");
}

test "send-keys encodes keys the way the program asked for them" {
    const s = try sockPath("send-keys-modes");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // Cursor keys in normal mode, then application mode (DECCKM), then the
    // kitty keyboard protocol's disambiguate flag.
    var p = try spawn(&.{ s, "--", "sh", "-c", "stty raw -echo; printf 'NORMAL\\r\\n'; head -c 3" ++ HEX ++
        "; printf '\\033[?1hAPPCURSOR\\r\\n'; head -c 6" ++ HEX ++
        "; printf '\\033[?1l\\033[>1uKITTY\\r\\n'; head -c 13" ++ HEX ++
        "; printf '\\033[>9uREPORTALL\\r\\n'; head -c 15" ++ HEX ++ "; exec cat" }, 24, 80);
    defer p.kill();

    alloc.free(try p.drainUntil("NORMAL", 3000));
    try sendKeys(s, &.{"Up"});
    try expectBytes(&p, " 1b 5b 41");

    alloc.free(try p.drainUntil("APPCURSOR", 3000));
    try sendKeys(s, &.{ "Up", "Home" });
    try expectBytes(&p, " 1b 4f 41 1b 4f 48");

    alloc.free(try p.drainUntil("KITTY", 3000));
    try sendKeys(s, &.{ "Escape", "C-c", "Enter" });
    try expectBytes(&p, " 1b 5b 32 37 75 1b 5b 39 39 3b 35 75 0d");

    // Asked to report every key, keys are reported; text is still text,
    // as from a paste or an input method.
    alloc.free(try p.drainUntil("REPORTALL", 3000));
    try sendKeys(s, &.{ "Space", "a", "Hi", "C-a" });
    try expectBytes(&p, " 1b 5b 33 32 75 61 48 69 1b 5b 39 37 3b 35 75");
}

test "capture-pane prints the screen and scrollback" {
    const s = try sockPath("capture");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // 5 rows of 20 columns: L1..L11 scroll off into scrollback.
    var p = try spawn(&.{
        s, "--", "sh", "-c",
        \\while [ "$(stty size)" != "5 20" ]; do sleep 0.05; done
        \\i=1; while [ $i -le 12 ]; do echo L$i; i=$((i+1)); done
        \\echo AAAAAAAAAAAAAAAAAAA BBBBBBBBBBBBBBBBBBB WRAPEND
        \\printf '\033[1;32mGREEN\033[0m'
        \\exec cat
    }, 5, 20);
    defer p.kill();
    alloc.free(try captureUntil(s, &.{}, "GREEN", 3000));

    try expectCapture(s, &.{}, "L12\nAAAAAAAAAAAAAAAAAAA\nBBBBBBBBBBBBBBBBBBB\nWRAPEND\nGREEN\n");
    try expectCapture(s, &.{"-p"}, "L12\nAAAAAAAAAAAAAAAAAAA\nBBBBBBBBBBBBBBBBBBB\nWRAPEND\nGREEN\n");
    try expectCapture(s, &.{"-J"}, "L12\nAAAAAAAAAAAAAAAAAAA BBBBBBBBBBBBBBBBBBB WRAPEND\nGREEN\n");
    // Negative lines reach into scrollback, '-' to its start, and lines
    // past either end are clamped. As in tmux, start and end may be swapped.
    try expectCapture(s, &.{ "-S", "-2", "-E", "0" }, "L10\nL11\nL12\n");
    try expectCapture(s, &.{ "-pJ", "-S-2", "-E0" }, "L10\nL11\nL12\n");
    try expectCapture(s, &.{ "-S", "-1000", "-E", "-1000" }, "L1\n");
    try expectCapture(s, &.{ "-S", "1", "-E", "0" }, "L12\nAAAAAAAAAAAAAAAAAAA\n");
    try expectCapture(s, &.{ "-S", "3", "-E", "100" }, "WRAPEND\nGREEN\n");
    try expectCapture(s, &.{ "-S", "-", "-E", "-10" }, "L1\nL2\n");
    {
        const r = try command(&.{ "capture-pane", s, "-S", "-" });
        defer r.deinit();
        try testing.expect(std.mem.startsWith(u8, r.stdout, "L1\nL2\nL3\n"));
        try testing.expect(std.mem.endsWith(u8, r.stdout, "L12\nAAAAAAAAAAAAAAAAAAA\nBBBBBBBBBBBBBBBBBBB\nWRAPEND\nGREEN\n"));
    }
    {
        // -e keeps styles as escape sequences; lines still end in \n.
        const r = try command(&.{ "capture-pane", s, "-e" });
        defer r.deinit();
        try testing.expectEqual(0, r.code);
        try testing.expect(contains(r.stdout, "\x1b[1m"));
        try testing.expect(contains(r.stdout, "GREEN"));
        try testing.expect(!contains(r.stdout, "\r"));
        try testing.expect(std.mem.endsWith(u8, r.stdout, "\n"));
    }

    try expectCommandFails(&.{ "capture-pane", s, "-x" }, "capture-pane: unknown option -x");
    try expectCommandFails(&.{ "capture-pane", s, "extra" }, "capture-pane: unexpected argument 'extra'");
    try expectCommandFails(&.{ "capture-pane", s, "-S" }, "capture-pane: -S needs a line");
    try expectCommandFails(&.{ "capture-pane", s, "-E", "end" }, "capture-pane: bad line 'end'");
    try expectCommandFails(&.{ "send-keys", s, "-x", "a" }, "send-keys: unknown option -x");
    const long = "1" ** 600;
    try expectCommandFails(&.{ "capture-pane", s, "-S", long }, "capture-pane: bad line '111");
}

test "capture-pane prints a line for every row" {
    const s = try sockPath("capture-rows");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // 6 rows of 20 columns. Row 0 is 19 A's and the wide 界, which doesn't
    // fit in the last column and so wraps to row 1.
    var p = try spawn(&.{
        s, "--", "sh", "-c",
        \\while [ "$(stty size)" != "6 20" ]; do sleep 0.05; done
        \\printf 'AAAAAAAAAAAAAAAAAAA\347\225\214X\n\nMID   \n'
        \\exec cat
    }, 6, 20);
    defer p.kill();
    alloc.free(try captureUntil(s, &.{}, "MID", 3000));

    // Blank rows are lines too, as in tmux, down to the bottom of the screen.
    try expectCapture(s, &.{}, "AAAAAAAAAAAAAAAAAAA\n界X\n\nMID\n\n\n");
    try expectCapture(s, &.{"-e"}, "AAAAAAAAAAAAAAAAAAA\n界X\n\nMID\n\n\n");
    try expectCapture(s, &.{ "-S", "2", "-E", "2" }, "\n");
    try expectCapture(s, &.{ "-S", "4", "-E", "5" }, "\n\n");
    // -J joins only within the range.
    // -J keeps trailing spaces the program printed, as tmux does.
    try expectCapture(s, &.{"-J"}, "AAAAAAAAAAAAAAAAAAA界X\n\nMID   \n\n\n");
    try expectCapture(s, &.{ "-J", "-S", "0", "-E", "0" }, "AAAAAAAAAAAAAAAAAAA\n");
    try expectCapture(s, &.{ "-J", "-S", "1", "-E", "1" }, "界X\n");
}

test "send-keys delivers more input than the pty holds" {
    // The pty echoes the input and tee prints it back, so tee stops reading
    // when nobody reads the pty. The server must keep reading the pty while
    // it writes. wc counts what got through.
    const s = try sockPath("send-keys-big");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "echo READY; tee /dev/tty | wc -c; exec cat" }, 24, 80);
    defer p.kill();
    alloc.free(try captureUntil(s, &.{}, "READY", 3000));

    // 64 KiB of lines (a quarter of that used to hang the session), then
    // end-of-file.
    const word = try alloc.alloc(u8, 32 * 1024);
    defer alloc.free(word);
    for (word, 0..) |*b, k| b.* = if (k % 64 == 63) '\n' else 'x';
    try sendKeys(s, &.{ "-l", word, word });
    try sendKeys(s, &.{"C-d"});
    alloc.free(try captureUntil(s, &.{}, "65536", 30000));
}

test "send-keys refuses input past a backlog" {
    const s = try sockPath("send-keys-backlog");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // A program that never reads.
    var p = try spawn(&.{ s, "--", "sh", "-c", "stty raw -echo; echo ASLEEP; exec sleep 1000" }, 24, 80);
    defer p.kill();
    alloc.free(try captureUntil(s, &.{}, "ASLEEP", 3000));

    const word = try alloc.alloc(u8, 120 * 1024);
    defer alloc.free(word);
    @memset(word, 'x');
    // 600 KB at a time: queued, queued, then refused past 1 MB.
    const big: []const []const u8 = &.{ "-l", word, word, word, word, word };
    try sendKeys(s, big);
    try sendKeys(s, big);
    try expectFails(try run("send-keys", s, big), "send-keys: the program hasn't read earlier input yet");
    // The session still answers.
    alloc.free(try captureUntil(s, &.{}, "ASLEEP", 3000));
}

test "input that never quite drains doesn't pile up" {
    const s = try sockPath("pty-inq-mem");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // A program reading 16 MiB in 16 KiB gulps, with a pause between, so a
    // client typing as fast as it can stays ahead of it the whole time.
    var p = try spawn(&.{ s, "--", "sh", "-c", "stty raw -echo; echo READY; i=0; while [ $i -lt 1024 ]; do head -c 16384 >/dev/null; i=$((i+1)); done; echo DRAINED; exec sleep 1000" }, 24, 80);
    defer p.kill();
    alloc.free(try captureUntil(s, &.{}, "READY", 3000));
    const server = try findServer(s, p.pid);
    const before = try rssKb(server);

    var cl = try RawClient.connect(s);
    defer cl.close();
    cl.sendWinch(24, 80);
    cl.attach();
    var chunk: [4096]u8 = undefined;
    @memset(&chunk, 'x');
    var sent: usize = 0;
    while (sent < 16 * 1024 * 1024) : (sent += chunk.len) cl.sendData(&chunk);

    // Sent, so all but the last 1 MiB or so has been written to the
    // program; that's all the queue may still hold.
    const after = try rssKb(server);
    alloc.free(try captureUntil(s, &.{}, "DRAINED", 60000));
    if (after > before + 8 * 1024) {
        std.debug.print("server RSS grew from {d} kB to {d} kB\n", .{ before, after });
        return error.TestUnexpectedResult;
    }
}

fn rssKb(pid: c_int) !usize {
    var pathbuf: [64]u8 = undefined;
    const path = try std.fmt.bufPrint(&pathbuf, "/proc/{d}/status", .{pid});
    const status = try readProcFile(path);
    defer alloc.free(status);
    var lines = std.mem.splitScalar(u8, status, '\n');
    while (lines.next()) |line| {
        if (!std.mem.startsWith(u8, line, "VmRSS:")) continue;
        const v = std.mem.trim(u8, line["VmRSS:".len..], " \tkB");
        return std.fmt.parseInt(usize, v, 10);
    }
    return error.NoRss;
}

test "capture-pane delivers a reply larger than the socket buffer" {
    // The client half-closes as soon as it has sent its command, so the
    // server reads EOF while most of a big reply is still queued. It must
    // finish sending before it hangs up.
    const s = try sockPath("capture-big");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    // Lines of alternating colors: as escape sequences, 500 of them come to
    // some 700 KB.
    var p = try spawn(&.{
        s, "--", "sh", "-c",
        \\while [ "$(stty size)" != "5 100" ]; do sleep 0.05; done
        \\l=''; i=0; while [ $i -lt 50 ]; do l="$l\033[31mx\033[32my"; i=$((i+1)); done
        \\i=0; while [ $i -lt 500 ]; do printf "$l\n"; i=$((i+1)); done
        \\echo BIGDONE
        \\exec cat
    }, 5, 100);
    defer p.kill();
    alloc.free(try captureUntil(s, &.{}, "BIGDONE", 10000));

    const r = try command(&.{ "capture-pane", s, "-e", "-S", "-" });
    defer r.deinit();
    try testing.expectEqualStrings("", r.stderr);
    try testing.expectEqual(0, r.code);
    try testing.expect(r.stdout.len > 512 * 1024);
    // All of it: the last line too, then the blank one the cursor is on.
    try testing.expect(std.mem.endsWith(u8, r.stdout, "BIGDONE\x1b[0m\n\n"));
}

test "commands explain a missing or outdated session" {
    try expectCommandFails(&.{"send-keys"}, "usage:");
    try expectCommandFails(&.{ "capture-pane", "-e" }, "usage:");

    const s = try sockPath("no-session");
    defer alloc.free(s);
    try expectCommandFails(&.{ "capture-pane", s }, "no session at");

    // A session server from before commands existed ignores MSG_COMMAND
    // and hangs up when the client does.
    {
        const old = try sockPath("old-session");
        defer alloc.free(old);
        defer cleanup(old);
        const pid = try fakeSession(old, "");
        defer _ = c.waitpid(pid, null, 0);
        try expectCommandFails(&.{ "send-keys", old, "x" }, "doesn't support send-keys; it was started by an older exe-scroll");
    }
    // One that hangs up partway through a reply is no such thing.
    {
        const gone = try sockPath("gone-session");
        defer alloc.free(gone);
        defer cleanup(gone);
        const pid = try fakeSession(gone, "\x01\x04\x00\x00\x00part");
        defer _ = c.waitpid(pid, null, 0);
        const msg = try std.fmt.allocPrint(alloc, "the session at {s} hung up", .{gone});
        defer alloc.free(msg);
        try expectCommandFails(&.{ "capture-pane", gone }, msg);
    }
}

/// Stand in for a session server at `path` that answers one connection with
/// `reply` and hangs up -- or, with no reply, reads until the client hangs
/// up. Returns the pid; it exits after that connection, or 10 seconds.
fn fakeSession(path: []const u8, reply: []const u8) !c_int {
    const l = std.c.socket(std.c.AF.UNIX, std.c.SOCK.STREAM, 0);
    try testing.expect(l >= 0);
    defer _ = std.c.close(l);
    var sa: std.c.sockaddr.un = undefined;
    sa.family = std.c.AF.UNIX;
    @memcpy(sa.path[0..path.len], path);
    sa.path[path.len] = 0;
    try testing.expect(std.c.bind(l, @ptrCast(&sa), @sizeOf(@TypeOf(sa))) == 0);
    try testing.expect(std.c.listen(l, 1) == 0);
    const pid = std.c.fork();
    if (pid == 0) {
        _ = c.alarm(10);
        const conn = std.c.accept(l, null, null);
        _ = std.c.write(conn, reply.ptr, reply.len);
        var buf: [4096]u8 = undefined;
        if (reply.len == 0) while (std.c.read(conn, &buf, buf.len) > 0) {};
        c._exit(0);
    }
    return pid;
}

test "commands need a well-formed payload and their own connection" {
    const s = try sockPath("cmd-proto");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "echo SCREEN; exec cat" }, 24, 80);
    defer p.kill();
    alloc.free(try p.drainUntil("SCREEN", 3000));

    // Every word ends in NUL: a last word without one is an error, not
    // silently dropped.
    {
        var cl = try RawClient.connect(s);
        defer cl.close();
        cl.sendFrame(RawClient.MSG_COMMAND, "send-keys\x00oops");
        try testing.expect(cl.waitDone(3000));
        try testing.expectEqualStrings("\x01malformed command", cl.done.?);
    }
    // An attached client's MSG_DATA is the terminal's output, so a command's
    // output has nowhere to go.
    {
        var cl = try RawClient.connect(s);
        defer cl.close();
        cl.sendWinch(24, 80);
        cl.attach();
        cl.sendFrame(RawClient.MSG_COMMAND, "capture-pane\x00");
        try testing.expect(cl.waitDone(3000));
        try testing.expectEqualStrings("\x01commands need a connection of their own", cl.done.?);
        try testing.expect(!contains(cl.data.items, "SCREEN"));
    }
}

test "capture-pane fails when it can't write its output" {
    const s = try sockPath("capture-full");
    defer alloc.free(s);
    defer cleanup(s);
    defer killServer(s);

    var p = try spawn(&.{ s, "--", "sh", "-c", "echo SCREEN; exec cat" }, 24, 80);
    defer p.kill();
    alloc.free(try captureUntil(s, &.{}, "SCREEN", 3000));

    const r = try std.process.run(alloc, io, .{ .argv = &.{ "sh", "-c", "exec \"$0\" capture-pane \"$1\" >/dev/full", opts.exe_path, s } });
    defer alloc.free(r.stdout);
    defer alloc.free(r.stderr);
    try testing.expectEqual(std.process.Child.Term{ .exited = 1 }, r.term);
    try testing.expect(contains(r.stderr, "capture-pane: can't write output"));
}
