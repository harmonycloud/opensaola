"""Regression cases for missing private access versus erroneous package image tags."""
import unittest
from matrix_registry import classify


class RegistryCauseTest(unittest.TestCase):
    def test_pull_evidence(self):
        for msg, expected in [
            ("Failed to authorize: 401 Unauthorized", "registry-authentication"),
            ("failed to fetch oauth token: 403 Forbidden", "registry-authentication"),
            ("dial tcp: lookup registry.internal: no such host", "registry-network"),
            ("dial tcp: i/o timeout", "registry-network"),
            ("manifest unknown", None),
            ("manifest not found", None),
            ("404 Not Found", None),
            ("ErrImagePull", None),
            ("ImagePullBackOff", None),
            ("no matching manifest for linux/arm64", None),
        ]:
            with self.subTest(message=msg):
                self.assertEqual(classify(msg), expected)


if __name__ == "__main__":
    unittest.main()
