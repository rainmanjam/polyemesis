"""Tests for polyemesis_obs.py's address rule. Run: python3 -m unittest
integrations/obs/test_polyemesis_obs.py (internal/testenv runs it too).

obspython exists only inside OBS, so a stub stands in for it."""

import os
import sys
import types
import unittest

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


if __name__ == "__main__":
    unittest.main()
