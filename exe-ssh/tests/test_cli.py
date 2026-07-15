import unittest
from unittest import mock

from exe_ssh import cli


class CLITest(unittest.TestCase):
    def test_random_session_avoids_existing_names(self):
        with mock.patch.object(cli.random, "choice", side_effect=lambda names: names[0]):
            self.assertNotEqual(cli.random_session_name(["amber"]), "amber")

    def test_random_session_falls_back_when_palette_is_exhausted(self):
        with mock.patch.object(cli.random, "choice", side_effect=lambda names: names[0]):
            self.assertEqual(cli.random_session_name(cli.SESSION_COLORS), "amber")


if __name__ == "__main__":
    unittest.main()
