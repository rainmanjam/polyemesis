/** Whether an MQTT broker URL uses a scheme with no TLS: mqtt://, tcp:// or
 *  ws://. The settings page says so beside the URL (credentials cross the
 *  network in the clear) and beside "accept self-signed" (which does nothing
 *  there). Mirrors MQTTSettings.Cautions in internal/db, which says the same in
 *  the server log on every connect. */
export function mqttPlainScheme(url: string | undefined): boolean {
  return /^(mqtt|tcp|ws):\/\//i.test((url ?? "").trim());
}
