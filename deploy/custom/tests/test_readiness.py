import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

import yaml


MODULE = Path(__file__).parents[1] / "readiness.py"


class DeploymentFilesTests(unittest.TestCase):
    def test_readiness_is_implemented(self):
        self.assertTrue(MODULE.is_file(), "custom deployment readiness is not implemented")


@unittest.skipUnless(MODULE.exists(), "readiness implementation is pending")
class ReadinessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not MODULE.exists():
            raise AssertionError("custom deployment readiness is not implemented")
        spec = importlib.util.spec_from_file_location("readiness", MODULE)
        cls.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.module)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = {
            "config-version": 8,
            "server": {"host": "0.0.0.0", "port": 8317},
            "management": {
                "allow-remote": True,
                "secret-key": "a" * 64,
                "disable-control-panel": False,
                "disable-auto-update-panel": True,
            },
            "access": {"api-keys": ["b" * 64]},
        }

    def validate(self):
        target = self.root / "config.yaml"
        target.write_text(yaml.safe_dump(self.config), encoding="utf-8")
        return self.module.validate_config(target)

    def test_accepts_strong_credentials(self):
        self.assertEqual(self.validate(), ["b" * 64])

    def test_existing_short_client_keys_remain_usable(self):
        self.config["access"]["api-keys"] = ["legacy-key"]
        self.assertEqual(self.validate(), ["legacy-key"])

    def test_existing_custom_client_keys_are_not_reinterpreted_as_templates(self):
        self.config["access"]["api-keys"] = ["your-api-key-custom"]
        self.assertEqual(self.validate(), ["your-api-key-custom"])

    def test_client_keys_follow_backend_whitespace_normalization(self):
        self.config["access"]["api-keys"] = ["", "  legacy-key  ", "  "]
        self.assertEqual(self.validate(), ["legacy-key"])

    def test_client_keys_preserve_supported_internal_spaces(self):
        self.config["access"]["api-keys"] = ["legacy client key"]
        self.assertEqual(self.validate(), ["legacy client key"])

    def test_rejects_empty_or_unusable_api_keys(self):
        for keys in ([], [""], ["  "], [123], ["b" * 64 + "\nunsafe"], ["b" * 64 + "中文"]):
            with self.subTest(keys=keys):
                self.config["access"]["api-keys"] = keys
                with self.assertRaises(ValueError):
                    self.validate()

    def test_rejects_insecure_management_configuration(self):
        for key, value in (
            ("secret-key", ""),
            ("secret-key", "short"),
            ("allow-remote", False),
            ("disable-control-panel", True),
            ("disable-auto-update-panel", False),
        ):
            with self.subTest(key=key):
                original = self.config["management"][key]
                self.config["management"][key] = value
                with self.assertRaises(ValueError):
                    self.validate()
                self.config["management"][key] = original

    def test_accepts_persisted_bcrypt_management_key(self):
        self.config["management"]["secret-key"] = "$2a$10$" + "x" * 53
        self.assertEqual(self.validate(), ["b" * 64])

    def test_management_and_client_keys_cannot_be_identical(self):
        self.config["management"]["secret-key"] = "b" * 64
        with self.assertRaises(ValueError):
            self.validate()

    def test_rejects_config_shape_without_printing_secrets(self):
        self.config["management"] = "secret-sensitive-value"
        with self.assertRaises(ValueError) as error:
            self.validate()
        self.assertNotIn("secret-sensitive-value", str(error.exception))

    def test_static_asset_must_match_release_manifest(self):
        page = self.root / "management.html"
        manifest = self.root / "release.json"
        page.write_text("<html>custom panel</html>", encoding="utf-8")
        import hashlib

        manifest.write_text(json.dumps({"ui_sha256": hashlib.sha256(page.read_bytes()).hexdigest()}))
        self.module.validate_static(self.root)
        page.write_text("<html>unexpected update</html>", encoding="utf-8")
        with self.assertRaises(ValueError):
            self.module.validate_static(self.root)


if __name__ == "__main__":
    unittest.main()
