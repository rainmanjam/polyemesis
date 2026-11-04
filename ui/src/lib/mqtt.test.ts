import { describe, expect, it } from "vitest";

import { mqttPlainScheme } from "./mqtt";

// The plaintext note used to fire only for mqtt://, so tcp:// and ws:// --
// equally unencrypted -- said nothing (poka-yoke audit 2026-11-04).
describe("mqttPlainScheme", () => {
  it.each(["mqtt://h:1883", "tcp://h:1883", "ws://h/mqtt", " MQTT://h"])("%s is plaintext", (u) => {
    expect(mqttPlainScheme(u)).toBe(true);
  });
  it.each(["mqtts://h:8883", "ssl://h:8883", "wss://h/mqtt", "", undefined])("%s is not", (u) => {
    expect(mqttPlainScheme(u)).toBe(false);
  });
});
