"""Exercise the source guard on synthetic Git repositories."""
import pathlib
import subprocess
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("check-neutrality.py").resolve()


class NeutralityTests(unittest.TestCase):
    """Prove both allowed project identity and rejected private values."""

    def check(self, content):
        """Run the real inventory scan on an untracked fixture."""
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            subprocess.run(["git", "init", "-q", directory], check=True)
            (root / "example.txt").write_text(content)
            return subprocess.run(
                ["python3", str(SCRIPT), directory], capture_output=True, text=True,
            )

    def test_neutral_values(self):
        result = self.check("\n".join((
            "go.xchunk.org/vela/internal/config", "https://docs.xchunk.org/vela",
            "store.example.com", "192.0.2.4", "198.51.100.3", "203.0.113.7",
            "127.0.0.1", "0.0.0.0", "::1", "[2001:db8::1]:8080",
            "127.0.0.1::9000", "172.17.0.1/32", "xchunk/vela:1.2.3",
        )))
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejected_values(self):
        for content in (
            "store." + "xchunk" + ".org", "prefix.go." + "xchunk" + ".org/vela", "go." + "xchunk" + ".org/other",
            "docs." + "xchunk" + ".org/velarium", "ghcr" + ".io/project",
            "GO" + "PRIVATE", "/ho" + "me/operator/data", "/Us" + "ers/operator/data",
            "8." + "8.8.8", "10." + "0.0.5", "2001:" + "4860" + "::" + "8888",
        ):
            with self.subTest(content=content):
                result = self.check(content)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("example.txt:1:", result.stderr)
                self.assertNotIn(content, result.stderr)


if __name__ == "__main__":
    unittest.main()
