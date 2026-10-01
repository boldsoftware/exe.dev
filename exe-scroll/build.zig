const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});

    // `-Dstrip` produces a stripped binary at link time. We strip in-toolchain
    // (rather than shelling out to the host `strip`) so cross-compiled builds
    // work: a Linux host's `strip` can't process a foreign-arch ELF, but Zig's
    // linker can strip any target it can emit. build-static.sh passes this.
    const strip = b.option(bool, "strip", "strip debug info from the binary") orelse false;

    const ghostty = b.dependency("ghostty", .{
        .target = target,
        .optimize = optimize,
    });

    const exe = b.addExecutable(.{
        .name = "exe-scroll",
        .root_module = b.createModule(.{
            .root_source_file = b.path("exe-scroll.zig"),
            .target = target,
            .optimize = optimize,
            .link_libc = true,
            .strip = strip,
        }),
    });
    exe.root_module.addImport("ghostty-vt", ghostty.module("ghostty-vt"));
    const version = sourceVersion(b);
    const info = b.addOptions();
    info.addOption([]const u8, "version", version);
    exe.root_module.addOptions("build_info", info);
    b.installArtifact(exe);

    // End-to-end tests (`zig build test`). They drive the real binary over a
    // pty, so we hand them its path via build options. The test step depends
    // on the executable, so `zig build test` (re)builds it first.
    const exe_opts = b.addOptions();
    exe_opts.addOptionPath("exe_path", exe.getEmittedBin());
    exe_opts.addOption([]const u8, "version", version);

    const tests = b.addTest(.{
        .root_module = b.createModule(.{
            .root_source_file = b.path("test_e2e.zig"),
            .target = target,
            .optimize = optimize,
            .link_libc = true,
        }),
    });
    tests.root_module.addImport("build_options", exe_opts.createModule());
    tests.root_module.addImport("ghostty-vt", ghostty.module("ghostty-vt"));

    const run_tests = b.addRunArtifact(tests);
    run_tests.has_side_effects = true; // always re-run, even if inputs unchanged
    const test_step = b.step("test", "Run end-to-end tests against the built binary");
    test_step.dependOn(&run_tests.step);
}

// sourceVersion names the exe-scroll source this binary was built from, as
// "0.<count>.9<tree>": <count> is the number of commits touching this
// directory (the release tag's minor version) and <tree> is the first 24 bits
// of the directory's git tree hash, in octal (`printf '%06x' 0<tree>` gives the
// hex back). release.sh tags releases with the same string, so `--version`
// names its release.
//
// It's a tree hash rather than a commit hash on purpose: the tree is the same
// whether this directory is built from the exe.dev monorepo or its oss mirror,
// and unrelated commits don't change it, so the bytes stay reproducible.
//
// The shape stays purely numeric because exed's `exe-scroll install` and the
// e1e tests parse it. Builds whose source git can't vouch for get a 0 in that
// slot: modified tracked files give 0.<count>.0, shallow clones (whose counts
// are wrong) 0.0.9<tree>, and no git at all 0.0.0.
fn sourceVersion(b: *std.Build) []const u8 {
    const tree = git(b, &.{ "rev-parse", "HEAD:./" }) orelse return "0.0.0";
    const tree24 = std.fmt.parseInt(u32, tree[0..@min(tree.len, 6)], 16) catch return "0.0.0";
    const shallow = git(b, &.{ "rev-parse", "--is-shallow-repository" });
    const count = if (shallow != null and std.mem.eql(u8, shallow.?, "false"))
        git(b, &.{ "rev-list", "--count", "HEAD", "--", "." }) orelse "0"
    else
        "0";
    const dirty = git(b, &.{ "status", "--porcelain", "--untracked-files=no", "--", "." });
    if (dirty == null or dirty.?.len != 0) return b.fmt("0.{s}.0", .{count});
    return b.fmt("0.{s}.9{o}", .{ count, tree24 });
}

// git runs git in this directory and returns its trimmed stdout, or null if it
// failed (no git, not a checkout, ...).
fn git(b: *std.Build, args: []const []const u8) ?[]const u8 {
    const argv = std.mem.concat(b.allocator, []const u8, &.{
        &.{ "git", "-C", b.build_root.path orelse "." },
        args,
    }) catch @panic("OOM");
    var code: u8 = undefined;
    const out = b.runAllowFail(argv, &code, .Ignore) catch return null;
    return std.mem.trim(u8, out, " \t\r\n");
}
