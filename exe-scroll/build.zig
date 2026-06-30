const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});

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
        }),
    });
    exe.root_module.addImport("ghostty-vt", ghostty.module("ghostty-vt"));
    b.installArtifact(exe);

    // End-to-end tests (`zig build test`). They drive the real binary over a
    // pty, so we hand them its path via build options. The test step depends
    // on the executable, so `zig build test` (re)builds it first.
    const exe_opts = b.addOptions();
    exe_opts.addOptionPath("exe_path", exe.getEmittedBin());

    const tests = b.addTest(.{
        .root_module = b.createModule(.{
            .root_source_file = b.path("test_e2e.zig"),
            .target = target,
            .optimize = optimize,
            .link_libc = true,
        }),
    });
    tests.root_module.addImport("build_options", exe_opts.createModule());

    const run_tests = b.addRunArtifact(tests);
    run_tests.has_side_effects = true; // always re-run, even if inputs unchanged
    const test_step = b.step("test", "Run end-to-end tests against the built binary");
    test_step.dependOn(&run_tests.step);
}
