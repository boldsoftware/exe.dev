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
