import io
import os
import subprocess
import unittest
from unittest import mock

from exe_ssh import cli


class FakeTTY(io.StringIO):
    def isatty(self):
        return True

    def fileno(self):
        return 2


class CLITest(unittest.TestCase):
    def setUp(self):
        cli._banner_rows = 0

    def test_random_session_avoids_existing_names(self):
        with mock.patch.object(cli.random, "choice", side_effect=lambda names: names[0]):
            self.assertNotEqual(cli.random_session_name(["amber"]), "amber")

    def test_random_session_falls_back_when_palette_is_exhausted(self):
        with mock.patch.object(cli.random, "choice", side_effect=lambda names: names[0]):
            self.assertEqual(cli.random_session_name(cli.SESSION_COLORS), "amber")

    def test_sign_failure_reason_is_last_stderr_line(self):
        err = subprocess.CalledProcessError(
            255, "ssh-keygen",
            stderr=b"Signing file (stdin)\nSigning (stdin) failed: agent refused operation\n")
        with mock.patch.object(cli.subprocess, "check_output", side_effect=err):
            with self.assertRaises(cli.SignError) as cm:
                cli.generate_token("/k", "v0@x.xterm.exe.xyz")
        self.assertIn("ssh-keygen sign failed", str(cm.exception))
        self.assertEqual(cm.exception.reason, "Signing (stdin) failed: agent refused operation")
        self.assertEqual(cli.SignError("x").reason, "")

    def banners(self, cols, *calls):
        """Run banner/log calls on a fake terminal cols wide (or each call's
        own width, given as a 4th element); return the output."""
        err = FakeTTY()
        with mock.patch.object(cli.sys, "stderr", err):
            for fn, args, kwargs, *width in calls:
                size = os.terminal_size((width[0] if width else cols, 24))
                with mock.patch.object(cli.os, "get_terminal_size", return_value=size):
                    fn(*args, **kwargs)
        return err.getvalue()

    def test_banner_redraws_over_previous_banner(self):
        out = self.banners(80,
                           (cli.banner, ("lost",), {"color": cli.YELLOW}),
                           (cli.banner, ("retry?",), {"color": cli.YELLOW, "replace": True}),
                           (cli.banner, ("retry?",), {"color": cli.YELLOW, "replace": True,
                                                      "note": "no key"}),
                           (cli.banner, ("retry?",), {"color": cli.YELLOW, "replace": True,
                                                      "note": "no key"}))
        self.assertEqual(out.count("\x1b[1A\r\x1b[J"), 2)
        self.assertEqual(out.count("\x1b[2A\r\x1b[J"), 1)

    def test_banner_redraws_all_rows_of_a_wrapped_banner(self):
        out = self.banners(20,
                           (cli.banner, ("\u26a1" * 12,), {"color": cli.YELLOW}),
                           (cli.banner, ("retry?",), {"color": cli.YELLOW, "replace": True}))
        # " exe-ssh: " + 12 double-width glyphs + " " is 35 columns: 2 rows.
        self.assertIn("\x1b[2A\r\x1b[J", out)

    def test_banner_does_not_redraw_after_resize(self):
        out = self.banners(40,
                           (cli.banner, ("lost",), {"color": cli.YELLOW}),
                           (cli.banner, ("retry?",), {"color": cli.YELLOW, "replace": True}, 100),
                           (cli.banner, ("retry?",), {"color": cli.YELLOW, "replace": True}, 100))
        self.assertEqual(out.count("A\r\x1b[J"), 1)

    def test_banner_does_not_redraw_over_other_output(self):
        out = self.banners(80,
                           (cli.banner, ("lost",), {"color": cli.YELLOW}),
                           (cli.log, ("plain",), {}),
                           (cli.banner, ("retry?",), {"color": cli.YELLOW, "replace": True}))
        self.assertNotIn("A\r\x1b[J", out)

    def test_banner_never_moves_cursor_off_a_tty(self):
        err = io.StringIO()
        with mock.patch.object(cli.sys, "stderr", err):
            cli.banner("one", color=cli.YELLOW)
            cli.banner("two", color=cli.YELLOW, replace=True)
        self.assertNotIn("A\r\x1b[J", err.getvalue())

    def wait_for_retry(self, *reads):
        """Answer the retry prompt with reads, one per os.read."""
        events = []
        with mock.patch.object(cli.sys, "stderr", io.StringIO()), \
                mock.patch.object(cli.termios, "tcflush",
                                  side_effect=lambda *a: events.append("flush")), \
                mock.patch.object(cli.os, "read",
                                  side_effect=lambda *a: events.append("read") or reads[
                                      events.count("read") - 1]):
            ok = cli.wait_for_retry(0)
        self.assertEqual(events[0], "flush")  # typeahead is dropped first
        return ok

    def test_wait_for_retry_on_r(self):
        self.assertTrue(self.wait_for_retry(b"x", b"\x1b[A", b"r"))
        self.assertTrue(self.wait_for_retry(b"R"))
        self.assertTrue(self.wait_for_retry(b"rr"))

    def test_wait_for_retry_quits_on_ctrl_c(self):
        self.assertFalse(self.wait_for_retry(b"x", b"\x03"))
        self.assertFalse(self.wait_for_retry(b"\x1b[99;5u"))  # kitty keyboard protocol
        self.assertFalse(self.wait_for_retry(b"\x1b[99;5:1u"))
        self.assertFalse(self.wait_for_retry(b"\x1b[99;133u"))  # Num Lock on
        self.assertFalse(self.wait_for_retry(b"\x1b[99;69u"))  # Caps Lock on
        self.assertFalse(self.wait_for_retry(b"\x1b[27;5;99~"))  # modifyOtherKeys
        self.assertFalse(self.wait_for_retry(b""))  # EOF

    def test_wait_for_retry_ignores_r_inside_other_input(self):
        self.assertFalse(self.wait_for_retry(b"\x1b[12;40R", b"rm -rf", b"\x03"))
        # Ctrl-Alt-C and Ctrl-9 aren't Ctrl-C.
        self.assertTrue(self.wait_for_retry(b"\x1b[99;7u", b"\x1b[57;5u", b"\x1b[999;5u", b"r"))


if __name__ == "__main__":
    unittest.main()
