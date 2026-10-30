# SRTLA: bonded IRL encoders

A bonded IRL encoder sends one stream over several network links at once, such
as two or three cellular modems and Wi-Fi. A stream that would break up on any
single modem then survives the drop-outs of each. BELABOX, Moblin and IRL Pro
all bond this way, using a protocol called **SRTLA** (SRT link aggregation).

polyemesis has a native SRTLA receiver. Point a bonded encoder at it and the
stream arrives at your programme as an ordinary SRT publish. Failover,
recording, destinations and [degraded-link failover](LINK-HEALTH.md) all work
the same.

## Setting it up

1. Under **Settings → Ingest → Listeners**, set the **SRTLA bonding port**.
   5000 is the usual choice. 0, the default, turns the receiver off.
2. Open that port as **UDP** in your firewall. In Docker, publish it, for
   example `-p 5000:5000/udp`; the commented line in `docker-compose.yml`
   shows where.
3. In the encoder, choose SRTLA and enter this server's address, the SRTLA port,
   and the programme's **publish token** as the stream ID. This is the same
   token you would use for plain SRT, shown on the programme's source card.

On BELABOX, use the custom SRTLA relay setting. On Moblin and IRL Pro, choose
an SRTLA URL with your token as the stream ID, for example
`srtla://your.server:5000?streamid=<token>`.

The receiver checks nothing itself. It passes the session to the SRT listener
on this machine, which checks the token exactly as it does for a direct SRT
publish. A wrong token is refused there. Repeated wrong tokens lock out
**the sender's own address**, not every bonded encoder, even though every
bonded session reaches the SRT listener from this machine.

## What you see

While a bonded encoder is publishing, the programme's source card shows the
merged session first: bitrate, RTT, loss and retransmits. Under it is one row
per network link, with the link's address, bitrate and share of the traffic.
When a modem loses signal, its row drops to nothing, usually before the total
bitrate falls far enough to trip degraded-link failover.

`polyemesis_srtla_links` on the [metrics endpoint](MONITORING.md) counts the
links per programme. The [Grafana dashboard](GRAFANA.md) graphs it.

## Limits

- At most **8 links** per bonded session, and **200** bonded sessions at once.
- A link that sends nothing for **10 seconds** is dropped. A session with no
  links left is removed 10 seconds later.
- One sender address can open at most a few new sessions per second, so one
  host cannot fill the session table.
- The receiver's port is one more open UDP port. It is off until you set it.

## How the protocol works

SRTLA sits in front of an ordinary SRT listener. This section describes it in
our own words, for anyone debugging a sender.

**Registering.** The sender makes up a random 256-byte identifier and sends its
first half in a **REG1** packet. The receiver creates a group, fills the second
half with its own random bytes, and returns the full identifier in a **REG2**.
The sender then sends a **REG2** with the full identifier from each of its
links. The receiver adds each address to the group and answers **REG3**.

The receiver answers with an error instead in these cases:

- **REG_NGP** means "no such group": the identifier is unknown. A sender that
  gets it starts again with REG1.
- **REG_ERR** means the address already belongs to another group, the group
  already has 8 links, or the table is full.

**Sending.** The sender spreads the SRT packets of one session across its links.
The receiver forwards each packet unchanged to the SRT listener, through one
socket per group. The SRT listener therefore sees one ordinary SRT peer per
bonded session.

For every 10 SRT data packets that arrive on a link, the receiver sends that
link an **SRTLA ACK** listing their sequence numbers. The sender uses these to
judge how much each link is carrying.

**Replies.** The SRT listener's acknowledgements go back over **every** link, so
the sender hears them promptly whichever link is still working. Every other
reply goes back over the link that most recently carried data.

**Keepalives.** Each link sends keepalives while it is idle. The receiver
echoes each one back unchanged, which is how the sender measures that link's
round trip.

Anything from an address that has not registered is dropped, and nothing is
sent back to it.

## Compatibility and testing

The receiver was tested against BELABOX's reference sender, `srtla_send`, built
from commit `37862da3d0c13b46956efd3f88877053293d97d6`, with an FFmpeg SRT
caller bonding over two links. To repeat the test:

```sh
scripts/srtla-interop/run.sh
```

A loopback test in `go test ./internal/srtla` bonds a real SRT session over two
links on every CI run.

BELABOX hardware, Moblin and IRL Pro have not been tested against this receiver
yet. They speak the same protocol as `srtla_send`, so they are expected to
work. If one does not, please open an issue with the encoder's version.

The receiver is our own Go code. BELABOX's `srtla` is licensed AGPL-3.0, and none
of its code was copied or translated. We learned the wire protocol by reading
the reference; this page and the implementation were written from that
knowledge. Its `srtla_send` is used only as a test partner, built inside a
throwaway container.
