*Read this in [Español](README.es-ES.md).*

> ## Security warning, read this before running anything
>
> **CloudAC ships with both of its network channels bound to loopback and will
> refuse to start otherwise unless you configure a shared secret.** That default
> is deliberate. Earlier versions bound every interface with no authentication
> at all, which meant anyone who could reach either port could kick any player
> or forge detection data.
>
> | Port | Listener | What an unauthenticated peer could do |
> |---|---|---|
> | **1234** | ComputationServer | Forge movement and abilities data for any player, turn off detection for themselves, or get an innocent player kicked |
> | **1212** | ACTransmitter, on your Minecraft server | Kick any player. Minecraft UUIDs are public, so no prior access is needed |
>
> If you run the two components on different machines, set `shared-secret` in
> `config.yml` and `CLOUDAC_SHARED_SECRET` on the daemon. Both are then
> authenticated with HMAC-SHA256 and messages carry a timestamp so a captured
> line cannot be replayed. **Restrict both ports, or neither is restricted:**
> the daemon sends its verdicts from its own host, so firewalling only 1212
> lets anyone who can reach 1234 have the daemon deliver a kick from an address
> your firewall already trusts.
>
> **TLS is not a substitute for the shared secret.** TLS without client
> certificates authenticates the *server* to the *client*, and both of the
> listeners here are the server, so an attacker simply speaks TLS and sends the
> same line encrypted. Authentication is the missing control, not encryption.
>
> **Privacy.** Both channels carry player UUIDs and a 20 Hz position feed. A
> Minecraft UUID resolves to a username through a public endpoint, so in the UK
> and EU this is personal data, and Minecraft's playerbase skews toward minors.
> Enable the shared secret before this crosses any network, and treat a DPIA as
> the starting point rather than an afterthought.
>
> This is a proof of concept. Read "Current limitations" below before running it
> on a server you care about.

# What is CloudAC

CloudAC is a proof of concept for a Minecraft anti-cheat that runs its checks
and data processing in a separate program, so the detection side can grow
without any of that growth landing on the Minecraft server's tick budget. More
checks, heavier processing, a different language, more memory: none of it costs
the game server anything. The game
server's job is reduced to shipping the relevant packet fields out and acting on
verdicts that come back.

### Why

Any anti-cheat, however well optimised, costs significantly more than an average
plugin. In severe cases it improves the player experience by banning cheaters
and degrades it through the lag it causes. Moving the expensive half off the box
is a real technique that several large networks use, and it deserves to be
better known.

### Technical details

CloudAC has two parts.

**ACTransmitter** is a Spigot plugin. It uses ProtocolLib to intercept the seven
packet types the checks need, serialises the relevant fields into a
pipe-delimited line, and hands it to a bounded queue. A dedicated thread drains
that queue over one long-lived connection. The plugin also owns the punishment
system, which acts on verdicts arriving on the return channel.

**ComputationServer** is a standalone Go daemon. It keeps a profile per online
player, updates it from each incoming line, runs the checks against it, and
sends a verdict back when one flags.

The wire format is one newline-terminated frame per message,
`v1|<hmac>|<payload>`, with the payload pipe-delimited. Field 0 of the payload
is a packet-type digit, field 1 the player UUID, field 2 a timestamp that the
speed check consumes to derive a real time delta. The transmitter dials the
ComputationServer on **TCP 1234**. The ComputationServer dials back on
**TCP 1212**.

The packet listener sends both the client's claimed ground state **and** the
server's authoritative view. That pairing matters: every security decision used
to rest on the client's claim alone.

#### Technical details, more choices

Another advantage of a separate program is that it can be written in whatever
language suits. Go was chosen here because it is compiled, modern, has
interfaces so a polymorphic check design still works, and had prior familiarity
behind it. Rust would be an equally good choice.

### What about the latency

There are two different latencies in a design like this, and keeping them apart
is the whole argument.

**Verdict round-trip latency** is the time from a suspicious packet arriving to
a punishment landing. CloudAC can afford this to be slow, and that is the
genuine advantage of a silent design. There are two ways to configure an
anti-cheat depending on your priority: banning the cheater, or preventing the
cheat. If your priority is banning, you do not want to set the cheater back the
moment you detect something, because a setback tells them they were caught and
they adapt. Better to keep them in the dark, collect evidence, and act once. An
anti-cheat that tolerates a slow verdict is free to move its checks off the game
server. This half of the argument holds.

**Blocking latency injected into the packet pipeline** is a different thing, and
it is the one that decides whether the design works. It is time the Minecraft
server spends *waiting*, on the thread processing a player's packets, before it
can get on with the tick. It affects every player rather than only cheaters, and
no amount of silence in the punishment design makes it cheaper.

That distinction is not academic here. An earlier version of this project opened
a new TCP connection for every single packet, inline on the Netty channel
thread. Measured against the current implementation on the same machine:

| Transport | ns per packet | bytes per packet | sockets |
|---|---|---|---|
| Connection per packet | ~500,000 | ~52,000 | 1 leaked |
| Persistent connection, flush each | ~15,000 | ~40 | 1 reused |
| **Bounded queue offer, drained off-thread** | **~500** | **0** | **0** |

Run `make bench` to reproduce that table. The first row swings by a factor of
two or so between runs, because it is dominated by connection setup and the OS
scheduler. The last row does not, because it never touches the network.

The hot path is now a queue offer. It cannot block, cannot allocate
significantly, and cannot throw, so network conditions can no longer affect
gameplay at all. That is the property this design claims and, in earlier
versions, did not have.

One honest note about what that buys. It removes a cost the transport
introduced. It does not by itself prove that distributing the checks bought
anything, because two checks this small were always cheap enough to run
in-process. The real case for the split is headroom: the detection side can grow
without the game server paying for it. That case is sound. It is simply not
demonstrated by two checks that would have cost nothing either way.

### Running it

```sh
make build          # both components
make test           # Go test suite
make race           # tests under the race detector
make fuzz           # fuzz the wire parser
make bench          # measure the transmitter hot path
make check          # gofmt, go vet, govulncheck
```

Copy `ACTransmitter-<version>.jar` into `plugins/`, start the server once to
generate `plugins/ACTransmitter/config.yml`, then run the daemon:

```sh
CLOUDAC_LISTEN_ADDR=127.0.0.1:1234 \
CLOUDAC_TRANSMITTER_ADDR=127.0.0.1:1212 \
./bin/cserver
```

Across machines, set `CLOUDAC_SHARED_SECRET` and the matching `shared-secret`
in `config.yml`. Generate one with `openssl rand -base64 32`.

Both sides log a heartbeat every 30 seconds carrying packet counts, tracked
profiles, and connection state. **A stalled packet count while players are
online is the alert condition.** It is the only signal that distinguishes "no
cheaters right now" from "detection has been dead since Tuesday".

`/actransmitter status` reports the same from in game. `/actransmitter punish
off` stops kicks while detection continues, which is the lever you want during
a false-positive incident.

### Current limitations

An honest list of what this does and does not do. A prototype should say this
out loud rather than leaving it to be discovered.

- **Do not use `/reload` with this plugin.** Use a full restart. A reload can
  leave the previous instance holding the verdict port, and the symptom is that
  verdicts silently stop arriving.
- **The speed check's constants are a first pass and are not validated against
  real player data.** They were chosen to separate the fastest legitimate
  airborne movement measured (0.40 blocks per tick) from an obvious speed
  cheat, and the test suite pins both ends. They need tuning against recorded
  traces before anyone relies on them.
- **Elytra and riptide movement legitimately exceeds the speed check's bounds**
  and is not currently excluded.
- **`CheckAbilities` only catches a client honest enough to announce a
  capability it was not granted.** A cheat client that flies via movement
  packets and omits the abilities packet does not trip it. Detecting the absence
  of a lie needs a different check.
- **The friction model detects acceleration, not sustained speed.** Very fast
  constant-velocity movement is caught, moderately fast constant-velocity
  movement is not.
- **The vendored ProtocolLib jar's provenance cannot be verified.** Its manifest
  reads `4.8.0-SNAPSHOT-b540` and the entire 4.x line has been removed upstream,
  so there is no copy left to compare against. `CHECKSUMS.txt` gives integrity
  going forward but cannot give authenticity looking backward. Substituting your
  own ProtocolLib is the better option.
- **Targets Minecraft 1.16.5.** The packet field indices in
  `WrapperPlayClientFlying` are ordinal positions in Mojang's field ordering and
  will silently return wrong values on a different version. Re-derive and
  re-verify them before porting.
- **The ComputationServer keeps state in memory only.** A restart is safe
  because the plugin re-announces every online player on reconnect, but there is
  no persistence and no second instance.

### Contributing

CloudAC is a prototype meant to show how designing this kind of system could
work. Contributions that expand and improve it are welcome. Current ToDo, in
roughly the order it should be worked:

1. **Validate the speed check's constants against recorded movement traces.**
   This is the highest-value work available. The test suite has the shape, it
   just needs real data.
2. **Add server-authoritative comparison as a check in its own right.** The wire
   already carries both the client's claimed ground state and the server's.
   Sustained disagreement is a stronger signal than either value alone, and it
   is what catches the most common cheat module directly.
3. **Migrate to ProtocolLib 5.x** so the vendored jar can be dropped and the
   dependency becomes visible to tooling again. Note that 5.3.0 is the last
   release supporting Java 8, so this does not force a JDK upgrade on its own.
4. **Improve data processing to be dynamic and not need to check packet type.**
   One caution: "dynamic" must not mean "send every field of every packet". The
   narrow registration list is what keeps the plugin's tick cost near zero.
5. **Exclude elytra and riptide states from the speed check.**
6. **Add more checks.** Follow the existing shape: a check returns a verdict, it
   does not dial a socket, and it reads server-authoritative fields wherever a
   security decision depends on them.
7. Your suggestions and ideas.

### Disclaimer

I did not invent the idea of running the brain of an anti-cheat separately from
the Minecraft server. That idea has existed for some time and is used by a few
big servers. The point of CloudAC is to make it more widely known, and hopefully
to inspire future projects to use it.

#### Disclaimer, current code state

The original code was written quickly and bodged together to some extent, since
publishing it was not the plan. It has since been reviewed in depth and largely
rewritten. The "Current limitations" section above is the honest remaining
list, and the tests cover the parts that used to be silently wrong.

<br/><br/>
<br/><br/>

![resized](https://user-images.githubusercontent.com/24839815/174480405-35d2422c-f1b8-4035-a7c2-ff34a2cfb89a.png)
