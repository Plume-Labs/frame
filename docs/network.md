# Link health

The node agent reads each machine's links from sysfs on every cycle where it can
list the node's disks, and writes them to `FrameMachine.status.network`. No command runs: the agent reads files
under the host's `/sys/class/net`, including the bonding state, so its command
list does not change. `/proc/net/bonding` is not used, because `/proc/net`
follows the reader's network namespace and the agent's pod has its own.

Only physical ports and bonds are reported. Lists are bounded like the storage
stacks: 32 ports, 8 bonds, 16 members per bond, with `*Omitted` counters.

| Condition and alert | Severity | True when |
|---|---|---|
| `NetworkDegraded` | critical | a bond is down, unless nobody brought it up, it has no member and the policy does not name it (the `bond0` the module makes when it loads); a bond holds fewer members than `members`; a link named in the policy is down or absent |
| `NetworkDegraded` | warning | a bond member is down, or sits outside the LACP aggregator the bond uses (it has link and carries nothing) |
| `LinkSpeedLow` | warning | a physical link that is up, or at the kernel's `unknown`, runs below its expected speed |
| `LinkSaturated` | warning | a link or bond averages `linkSaturatedPercent` of its speed over five minutes, in its busier direction (default 80 %) |
| `LinkErrorsRising` | warning | a link or bond counts `linkErrorsPerMinute` errors a minute over five minutes, rounded up (at the default of 1, a single error raises it for five minutes) |

A bond member's alert is on `<bond>/<member>`, the others on the interface. A port
that is down and not named in the policy raises nothing: machines carry unused
ports. The rule is the one of [storage alerts](storage.md#storage-health-alerts):
a failed read, a silent agent, or a report that no longer mentions a device
neither raises nor clears anything, and a link that is down keeps its open
`LinkSpeedLow`, having no speed to read.

A field that reads empty is a read that said nothing, not a healthy one: a link
with no state, a named one at the kernel's `unknown`, a bond with no mode, a bond
or member with no status, an
LACP aggregator of 0 or `N/A`, a bond whose member list could not be read. Each
keeps what it had open and makes `NetworkDegraded` `Unknown`, reason
`ReadUnavailable`. A port with no state does so for `LinkSpeedLow` too, as does a
report that lists no port and no bond. A link that is up and gives no
speed, the case of every virtio NIC, makes `LinkSpeedLow` `Unknown`, reason
`SpeedNotReported`. An SR-IOV virtual function is not a port, and is not listed. A link at `unknown`
that shows a speed has it compared; one that shows none owes nothing. Only ports
and bonds are listed: a bridge, a VLAN or a VF named in the policy reads as absent.

The agent reads links in the background, so a driver that hangs does not hold up
its tick: the last report ages until the condition goes `Unknown`.

An alert for a link absent from the node ends when the policy stops naming it, so
a typo in a name can be corrected. `speedMbps` applies to a physical port: on a
bond it does nothing, name the bond's ports. If the policy itself cannot be read,
links go `Unknown` and storage goes on. An alert held open has its "Removed on
purpose" in Storage → Health, for an admin, like the storage ones. An alert
raised on a named link whose driver then stays at `unknown` needs it to close.

A member that left a bond altogether is in no list, so only `members` in the
[`NetworkHealthPolicy`](crd-reference.md#networkhealthpolicy) can notice it. Error
and drop counters are totals since the driver loaded, which is why `LinkErrorsRising`
reads their rate and not them. Drops are shown and not alerted on: `rx_dropped`
counts frames nobody wanted as well as frames lost.

## Traffic

The agent keeps each link's counters in memory for 15 minutes, read every 30
seconds, and writes `traffic` beside the port or bond: bytes a second over the last
minute, the busiest interval of the last 15, and utilisation against the link's
speed or the bond's total, in the busier direction. The history dies with the
agent and starts over when a driver resets its counters, or after a gap of over 15
minutes. A shorter gap gives the average across it.

A link with no rates yet, or under five minutes of history, makes `LinkSaturated`
and `LinkErrorsRising` `Unknown`, reason `ReadUnavailable`, and keeps what was open,
so an agent restart closes nothing; a counter that cannot be read costs that reading
alone, and the next one spans the gap. A link that gives no speed, the case of every
virtio NIC, has its bytes but no percentages: `SpeedNotReported`, the same `Unknown`.
One at the kernel's `unknown` owes none, as for `LinkSpeedLow`. A link that goes down
keeps both alerts open, having nothing to read, until it comes back or "Removed on
purpose" closes them. An open `LinkSaturated` clears five points under the threshold. Saturation of a bond's
member with the bond itself far from full is a hash that does not spread.

The manager publishes `frame_link_utilization_percent`,
`frame_link_bytes_per_second{direction}` and `frame_link_errors_per_minute`, by
machine and interface, updated at each pass over the machine, about a minute, and
dropped once the report is ten minutes old. A machine whose BMC is silent is passed
over less often, 30 minutes at most, and keeps its last figures until then. Prometheus needs
`metrics.serviceMonitor.enabled` to see them. Network → Bottlenecks ranks the links by their 15-minute peak,
and lists the pods sending and receiving the most from Prometheus' cAdvisor
counters (`container_network_*_bytes_total`). Pods on the host network are left
out, since cAdvisor gives them the node's interfaces. Nothing here limits anything.

Expectations are edited in Network → Links, through `frame-uiproxy`, and reach each
machine at its next pass.
