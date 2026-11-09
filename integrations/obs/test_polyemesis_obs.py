"""Tests for polyemesis_obs.py's address rule. Run: python3 -m unittest
integrations/obs/test_polyemesis_obs.py (internal/testenv runs it too).

obspython exists only inside OBS, so a stub stands in for it."""

import contextlib
import io
import os
import ssl
import sys
import tempfile
import types
import unittest
import urllib.error
from unittest import mock

stub = types.ModuleType("obspython")
stub.settings = {}
stub.obs_data_get_string = lambda s, k: s.get(k, "")
stub.obs_data_get_bool = lambda s, k: bool(s.get(k, False))
sys.modules["obspython"] = stub
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import polyemesis_obs as bridge  # noqa: E402


class AddressRule(unittest.TestCase):
    """The bearer token goes only over https://, or http:// to this machine
    (poka-yoke audit 2026-11-04)."""

    def test_https_anywhere_is_accepted(self):
        self.assertEqual(bridge.parse_server("https://vps.example.com:8080/"),
                         ("https://vps.example.com:8080", None))

    def test_http_to_this_machine_is_accepted(self):
        for raw in ("http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"):
            addr, refusal = bridge.parse_server(raw)
            self.assertIsNone(refusal, raw)
            self.assertEqual(addr, raw)

    def test_http_to_another_host_is_refused(self):
        for raw in ("http://vps.example.com:8080", "http://192.168.1.20:8080", "http://localhost.evil.example"):
            addr, refusal = bridge.parse_server(raw)
            self.assertEqual(addr, "", raw)
            self.assertIn("unencrypted", refusal)

    def test_other_schemes_are_refused(self):
        for raw in ("ftp://vps.example.com", "vps.example.com:8080", "file:///etc/passwd"):
            addr, refusal = bridge.parse_server(raw)
            self.assertEqual(addr, "", raw)
            self.assertIsNotNone(refusal, raw)

    def test_a_refused_address_sends_nothing(self):
        bridge.script_update({"server": "http://vps.example.com:8080", "token": "obs-token"})
        self.assertEqual(bridge._server, "")
        sent = []
        real = bridge.urllib.request.urlopen
        bridge.urllib.request.urlopen = lambda *a, **k: sent.append(a)
        try:
            bridge._send({"event": "stream.started"})
        finally:
            bridge.urllib.request.urlopen = real
        self.assertEqual(sent, [], "a request was made to a refused address")


class FakeContext:
    """Stands in for an ssl.SSLContext: only the store count and the bundle it
    was built from matter."""

    def __init__(self, cafile=None, ca_count=0):
        self.cafile = cafile
        self.ca_count = ca_count

    def cert_store_stats(self):
        return {"x509": self.ca_count, "x509_ca": self.ca_count, "crl": 0}


class CABundleFallback(unittest.TestCase):
    """python.org's macOS Python has an empty default store, so every report
    failed with CERTIFICATE_VERIFY_FAILED against a valid site. Fall back to a
    bundle that exists; never switch verification off."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.paths = []
        for name in ("a.pem", "b.pem"):
            path = os.path.join(self.tmp.name, name)
            open(path, "w").close()
            self.paths.append(path)
        self.missing = os.path.join(self.tmp.name, "missing.pem")
        self.default = FakeContext(ca_count=0)
        self.built = []
        bridge._server = "https://vps.example.com:8080"
        bridge._ca_file = ""
        bridge._bundle_logged = False
        for patcher in (
            mock.patch.object(ssl, "create_default_context", side_effect=self._create),
            mock.patch.object(bridge, "SYSTEM_CA_BUNDLES", (self.missing, self.paths[0], self.paths[1]), create=True),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.addCleanup(setattr, bridge, "_ca_file", "")

    def _create(self, cafile=None, **kw):
        self.built.append(cafile)
        return self.default if cafile is None else FakeContext(cafile=cafile, ca_count=1)

    def _no_certifi(self):
        patcher = mock.patch.dict(sys.modules, {"certifi": None})
        patcher.start()
        self.addCleanup(patcher.stop)

    def _context(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            ctx = bridge._context()
        return ctx, out.getvalue()

    def test_empty_store_falls_back_to_certifi(self):
        fake = types.ModuleType("certifi")
        fake.where = lambda: self.paths[1]
        with mock.patch.dict(sys.modules, {"certifi": fake}):
            ctx, log = self._context()
        self.assertEqual(ctx.cafile, self.paths[1])
        self.assertIn(self.paths[1], log)
        self.assertIn("certifi", log)

    def test_without_certifi_uses_the_first_existing_system_path(self):
        self._no_certifi()
        ctx, log = self._context()
        self.assertEqual(ctx.cafile, self.paths[0])  # the missing one is skipped
        self.assertIn(self.paths[0], log)

    def test_the_bundle_is_logged_once(self):
        self._no_certifi()
        _, first = self._context()
        _, second = self._context()
        self.assertNotEqual(first, "")
        self.assertEqual(second, "")

    def test_a_populated_store_is_left_alone(self):
        self.default.ca_count = 140
        ctx, log = self._context()
        self.assertIs(ctx, self.default)
        self.assertEqual(self.built, [None])
        self.assertEqual(log, "")

    def test_an_explicit_ca_file_wins(self):
        bridge._ca_file = self.paths[1]
        fake = types.ModuleType("certifi")
        fake.where = lambda: self.paths[0]
        with mock.patch.dict(sys.modules, {"certifi": fake}):
            ctx, log = self._context()
        self.assertEqual(ctx.cafile, self.paths[1])
        self.assertEqual(self.built, [self.paths[1]])
        self.assertEqual(log, "")

    def test_nothing_found_keeps_the_default_and_its_failure_message(self):
        self._no_certifi()
        with mock.patch.object(bridge, "SYSTEM_CA_BUNDLES", (self.missing,), create=True):
            ctx, log = self._context()
            self.assertIs(ctx, self.default)
            self.assertEqual(log, "")
            err = urllib.error.URLError(ssl.SSLCertVerificationError("unable to get local issuer certificate"))
            out = io.StringIO()
            with mock.patch.object(bridge.urllib.request, "urlopen", side_effect=err), contextlib.redirect_stdout(out):
                self.assertFalse(bridge._send({"event": "stream.started"}))
        self.assertIn("polyemesis unreachable for stream.started", out.getvalue())
        self.assertIn("unable to get local issuer certificate", out.getvalue())


if __name__ == "__main__":
    unittest.main()
