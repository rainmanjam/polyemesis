# Link health: degraded-link failover, live link stats and chat commands

An IRL stream rarely fails by disconnecting. The cellular uplink gets worse
first: the bitrate falls, the round trip grows, packets go missing, and the
picture breaks up for minutes before the encoder finally gives up. Ordinary
failover only moves when bytes stop arriving, so it sits through all of that.

This page covers three things that deal with a link that is still connected
but failing:

- [Degraded-link failover](#degraded-link-failover) leaves the primary when it
  falls below your thresholds.
- [Live link stats](#live-link-stats) show bitrate, RTT, loss and retransmits on
  the source card.
- [Chat commands](#chat-commands) let your moderators check the bitrate and
  switch sources from chat.

Bonding several cellular links into one stream is a separate feature; see
[SRTLA.md](SRTLA.md).

## Degraded-link failover

Turn it on under **Settings → Pipeline → Failover → Leave a degraded link**.
Failover itself has to be on, with somewhere to go: a backup encoder, a
playlist, or the slate.

| Threshold | Default | Measured from |
|---|---|---|
| Bitrate below (kbps) | 800 | the bytes arriving on the primary's relay, for every ingest mode |
| Round-trip time above (ms) | 1500 | SRT's own smoothed RTT; **SRT and SRTLA only** |
| Packet loss above (%) | 15 | the share of SRT data packets reported missing over the last second or so; **SRT and SRTLA only** |
| For at least (seconds) | 5 | how long a breach must last |

Each threshold is independent, and **0 switches that one off**. An RTMP or pull
encoder has no RTT or loss to measure, so for those only the bitrate applies.
Their RTT and loss are reported as absent, never as 0.

### What happens when a link trips

When a breach has lasted the whole sustain time, the primary counts as
unavailable and the selector does what it would do for a dead one:

1. If a backup encoder is delivering, the backup goes on air.
2. Otherwise, if the playlist is running, the playlist goes on air.
3. Otherwise, if the slate is on, the slate goes on air.
4. Otherwise the primary **stays on air**. A poor picture beats none. The
   source card, status and MQTT still say the link is degraded.

The reason starts `degraded: `, then says what the selector did and, in
brackets, what was measured:

```
degraded: the primary ingest's link is degraded and no backup is on air (bitrate 640 kbps below 800 kbps for 5s)
```

That reason reaches every place a failover reason does: the dashboard, the
[events stream](EVENTS.md#ingest-and-failover) and the `failover.switched`
[webhook](HOOKS.md), the stream timeline and the `failover.switched` alert,
[MQTT](MQTT.md) (`failoverReason` and `degraded`), and the
[Stream Deck](STREAM-DECK.md) failover key.

### Coming back

The link has to read healthy for **the same sustain time** before it counts as
recovered. Without that, a link hovering around a threshold would flip the
broadcast between the slate and the primary every few seconds.

After that, your existing return rule applies:

- From the slate or the playlist, the primary comes back as soon as it has
  recovered.
- From the backup, **manual** return (the default) keeps the backup on air
  until you switch back. **Automatic** return waits *Return after* seconds,
  counted from the moment the link recovered, not from when the encoder
  connected.

If you pin the primary by hand, the pin holds even while the link is degraded.
You have seen the picture and chosen it.

Only the primary is judged. A degraded backup stays on air, because nothing
better is available when it is the one on air.

## Live link stats

While an SRT encoder is publishing, its source card shows:

- **bitrate**, measured over the last second or so;
- **RTT**, the smoothed round trip;
- **loss**, the packets missing since the encoder connected, and in brackets
  the share lost over the last second or so;
- **retrans**, the retransmitted packets received.

A bonded encoder also shows one row per link; see [SRTLA.md](SRTLA.md).

An RTMP encoder shows who is connected and since when. TCP has no RTT or loss to
report.

For Prometheus, `polyemesis_failover_degraded`, `polyemesis_failover_active`,
`polyemesis_ingest_link_rtt_seconds`, `polyemesis_ingest_link_loss_ratio` and
`polyemesis_srtla_links` are described in
[MONITORING.md](MONITORING.md#failover-and-link-health). The
[Grafana dashboard](GRAFANA.md) graphs them in its Ingest row.

## Chat commands

Turn them on under **Settings → Pipeline → Chat commands**, and choose the
programme they act on. They are off by default, because they let every moderator
of every connected channel change what is on air.

| Command | Who | What it does |
|---|---|---|
| `!bitrate` | anyone | Replies with the bitrate, whether the link is degraded and why, and what is on air. |
| `!switch primary` / `backup` / `playlist` / `slate` | moderators and the broadcaster | Puts that source on air, as the console's source switch does. |
| `!switch auto` | moderators and the broadcaster | Clears the manual choice and lets failover decide again. |
| `!fix` | moderators and the broadcaster | Clears the manual choice and restarts the feed that is on air. |

The replies go out through the console's chat send, to every connected platform.

- A viewer who types `!switch` or `!fix` gets **no reply**. Otherwise chat could
  make polyemesis spam its own channel.
- Only platforms that tell polyemesis who is a moderator or the broadcaster
  allow the moderator commands. Where a platform does not say, nobody may run
  them.
- Each command can run at most once every 10 seconds, because every reply
  costs YouTube quota.
- Messages older than two minutes are ignored, so a reconnect that replays old
  chat cannot replay old commands. Polyemesis never acts on its own messages.
- Every `!switch` and `!fix` is written to the server log with the platform and
  the moderator's name. The switch itself then appears on the timeline like any
  other source switch.

`!fix` follows the IRL convention of "refresh the media source". It does not
reconnect your encoder, which only your encoder can do. It replaces the
polyemesis process reading it, which clears a stuck or garbled feed.
