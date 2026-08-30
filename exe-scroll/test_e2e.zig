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
    extern "c" fn __errno_location() *c_int;
    fn errno() c_int {
        return __errno_location().*;
    }

    const SIGKILL = 9;
    const SIGUSR1 = 10;
    const SIGUSR2 = 12;
    const EINTR = 4;
    const EAGAIN = 11;
    const POLLIN: c_short = 0x001;
    const WNOHANG = 1;

    // Set the size of a pty via its master fd (what a terminal emulator does
    // on window resize). The kernel then delivers SIGWINCH to the pty's
    // foreground process group. Same encoding dance as exe-scroll.zig.
    const TIOCSWINSZ: c_int = @bitCast(@as(u32, @intCast(std.c.T.IOCSWINSZ)));
};

const alloc = std.heap.c_allocator;

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
        const deadline = std.time.milliTimestamp() + ms;
        while (std.time.milliTimestamp() < deadline) {
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
        const deadline = std.time.milliTimestamp() + ms;
        while (std.time.milliTimestamp() < deadline) {
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
        const deadline = std.time.milliTimestamp() + ms;
        while (true) {
            const r = c.waitpid(self.pid, null, c.WNOHANG);
            if (r == self.pid) {
                self.pid = -1;
                return true;
            }
            if (std.time.milliTimestamp() >= deadline) return false;
            std.Thread.sleep(10 * std.time.ns_per_ms);
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
    std.fs.cwd().access(path, .{}) catch return false;
    return true;
}

fn cleanup(path: []const u8) void {
    std.fs.cwd().deleteFile(path) catch {};
}

/// Poll for a path to (dis)appear, up to `ms`. Returns true if the predicate
/// held before the deadline.
fn waitFor(path: []const u8, want_exists: bool, ms: i32) bool {
    const deadline = std.time.milliTimestamp() + ms;
    while (std.time.milliTimestamp() < deadline) {
        if (fileExists(path) == want_exists) return true;
        std.Thread.sleep(10 * std.time.ns_per_ms);
    }
    return fileExists(path) == want_exists;
}

/// Find the detached session-server pid for `socket`: the exe-scroll process
/// that rewrote its command line to "exe-scroll: session <socket>" (see
/// setProcTitle in exe-scroll.zig), other than `exclude` (the foreground
/// attach client). Returns 0 if none found.
fn findServer(socket: []const u8, exclude: c_int) !c_int {
    var dir = try std.fs.openDirAbsolute("/proc", .{ .iterate = true });
    defer dir.close();
    var needlebuf: [512]u8 = undefined;
    const needle = try std.fmt.bufPrint(&needlebuf, "exe-scroll: session {s}", .{socket});
    var it = dir.iterate();
    while (try it.next()) |entry| {
        const pid = std.fmt.parseInt(c_int, entry.name, 10) catch continue;
        if (pid == exclude) continue;
        var pathbuf: [64]u8 = undefined;
        const cmdpath = std.fmt.bufPrint(&pathbuf, "/proc/{s}/cmdline", .{entry.name}) catch continue;
        const data = std.fs.cwd().readFileAlloc(alloc, cmdpath, 64 * 1024) catch continue;
        defer alloc.free(data);
        // The retitled command line is NUL-padded; the role + socket live in
        // the first NUL-terminated chunk.
        var parts = std.mem.splitScalar(u8, data, 0);
        const arg0 = parts.next() orelse continue;
        if (std.mem.eql(u8, arg0, needle)) return pid;
    }
    return 0;
}

fn contains(haystack: []const u8, needle: []const u8) bool {
    return std.mem.indexOf(u8, haystack, needle) != null;
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

    fd: c_int,
    rbuf: std.ArrayListUnmanaged(u8) = .empty, // raw inbound bytes
    data: std.ArrayListUnmanaged(u8) = .empty, // reassembled MSG_DATA payloads

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
        self.sendFrame(MSG_ATTACH, &.{0}); // REPLAY_NONE
    }

    /// Send terminal input (as if typed).
    fn sendData(self: *RawClient, bytes: []const u8) void {
        self.sendFrame(MSG_DATA, bytes);
    }

    /// Read server frames until `needle` appears in the concatenated MSG_DATA
    /// payloads or `ms` elapses. Returns true if found. Payloads accumulate
    /// across calls, so a needle already received is found immediately.
    fn drainUntil(self: *RawClient, needle: []const u8, ms: i32) bool {
        const deadline = std.time.milliTimestamp() + ms;
        while (true) {
            if (std.mem.indexOf(u8, self.data.items, needle) != null) return true;
            const left = deadline - std.time.milliTimestamp();
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
                    if (self.rbuf.items[pos] == MSG_DATA)
                        self.data.appendSlice(alloc, self.rbuf.items[pos + 5 .. pos + 5 + len]) catch return false;
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
    const deadline = std.time.milliTimestamp() + 2000;
    while (std.time.milliTimestamp() < deadline) {
        if ((findServer(socket, 0) catch 0) <= 0) return;
        std.Thread.sleep(10 * std.time.ns_per_ms);
    }
}

// ----------------------------------------------------------------------------
// Tests.
// ----------------------------------------------------------------------------

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

test "secure parent dirs are created 0700" {
    seq += 1;
    const base = try std.fmt.allocPrint(alloc, "/tmp/exe-scroll-zt-{d}-{d}-dirs", .{ std.c.getpid(), seq });
    defer alloc.free(base);
    std.fs.cwd().deleteTree(base) catch {};
    defer std.fs.cwd().deleteTree(base) catch {};

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
        const st = try std.fs.cwd().statFile(d);
        try testing.expectEqual(@as(u16, 0o700), @as(u16, @intCast(st.mode & 0o777)));
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

    const ino_before = (try std.fs.cwd().statFile(s)).inode;

    // Delete the socket out from under the live session.
    cleanup(s);
    try testing.expect(!fileExists(s));
    // The server must NOT recreate it on its own (no timer/watcher).
    std.Thread.sleep(800 * std.time.ns_per_ms);
    try testing.expect(!fileExists(s));

    // Signal the server: it should rebind a fresh socket.
    _ = c.kill(server, c.SIGUSR1);
    try testing.expect(waitFor(s, true, 2000));
    const ino_after = (try std.fs.cwd().statFile(s)).inode;
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

    // B attaches at 30x100. Its attach-time WINCH must NOT resize the PTY:
    // A owns the size and B hasn't typed anything yet.
    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    const b0 = try b.drain(1000);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "24 80")); // still A's size
    try testing.expect(!contains(b0, "30 100")); // B's WINCH was not applied

    // B types: that claims ownership and applies B's recorded 30x100.
    b.write("x");
    const b1 = try b.drainUntil("30 100", 3000);
    defer alloc.free(b1);
    try testing.expect(contains(b1, "30 100"));

    // A resizes to 40x120. B owns the size now, so A's WINCH is recorded but
    // not applied.
    a.resize(40, 120);
    const a1 = try a.drain(1000);
    defer alloc.free(a1);
    try testing.expect(contains(a1, "30 100")); // still B's size
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

    // A creates (and owns) the session; B and C attach at other sizes, which
    // must not disturb the PTY.
    var a = try spawn(&.{ s, "--", "sh", "-c", SIZE_PRINTER }, 24, 80);
    const a0 = try a.drainUntil("24 80", 3000);
    defer alloc.free(a0);
    try testing.expect(contains(a0, "24 80"));

    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    var cc = try spawn(&.{s}, 35, 110);
    defer cc.kill();
    const b0 = try b.drain(1000);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "24 80"));
    try testing.expect(!contains(b0, "30 100"));
    try testing.expect(!contains(b0, "35 110"));

    // The owner detaches: the size becomes unowned. Nobody inherits it
    // automatically -- the next WINCH (or input) claims it.
    a.signal(c.SIGUSR2);
    const bye = try a.drainUntil("detached", 2000);
    defer alloc.free(bye);
    try testing.expect(a.waitExit(2000));
    a.kill();
    std.Thread.sleep(200 * std.time.ns_per_ms); // let the server process it

    // B's resize is the first WINCH after the release: it applies, and B
    // becomes the owner.
    b.resize(50, 150);
    const b1 = try b.drainUntil("50 150", 3000);
    defer alloc.free(b1);
    try testing.expect(contains(b1, "50 150"));

    // C's resize must now be ignored: B owns the size (and C never typed).
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

    // B attaches at 30x100 (recorded, not applied).
    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    const settle = try b.drain(600);
    alloc.free(settle);

    // Emulator auto-replies riding the input path: focus-in report, a DA1
    // response, and a cursor position report. None of these are typing, so
    // none may claim size ownership.
    b.write("\x1b[I");
    b.write("\x1b[?62c");
    b.write("\x1b[12;40R");
    // A CPR split across two input frames: the classifier state must carry
    // across MSG_DATA payloads for the second half to stay an auto-reply.
    b.write("\x1b[12;");
    std.Thread.sleep(150 * std.time.ns_per_ms);
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

    var b = try spawn(&.{s}, 30, 100);
    defer b.kill();
    const b0 = try b.drain(800);
    defer alloc.free(b0);
    try testing.expect(contains(b0, "24 80"));
    try testing.expect(!contains(b0, "30 100")); // A owns; B's WINCH ignored

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
    std.Thread.sleep(200 * std.time.ns_per_ms);

    // A raw protocol client claims the unowned size with a WINCH and never
    // attaches (frozen in the built-in client's WINCH-before-ATTACH window).
    var r = try RawClient.connect(s);
    defer r.close();
    r.sendWinch(50, 150);
    std.Thread.sleep(500 * std.time.ns_per_ms); // let a "50 150" line print

    // B attaches at 30x100. Its attach-time WINCH displaces the stale
    // (never-attached) owner immediately.
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
    std.Thread.sleep(200 * std.time.ns_per_ms);

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
    std.Thread.sleep(300 * std.time.ns_per_ms); // let the server process it

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
    const settle = try b.drain(600);
    alloc.free(settle);

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
    const settle = try b.drain(600);
    alloc.free(settle);

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
