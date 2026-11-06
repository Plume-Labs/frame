# Link health

The node agent reads each machine's links from sysfs on every cycle and writes
them to `FrameMachine.status.network`. No command runs: the agent reads files
under the host's `/sys/class/net`, including the bonding state, so its command
list does not change. `/proc/net/bonding` is not used, because `/proc/net`
follows the reader's network namespace and the agent's pod has its own.

Only physical ports and bonds are reported. Lists are bounded like the storage
stacks: 32 ports, 8 bonds, 16 members per bond, with `*Omitted` counters.

| Condition and alert | Severity | True when |
|---|---|---|
| `NetworkDegraded` | critical | a bond is down; a bond holds fewer members than `members`; a link named in the policy is down or absent |
| `NetworkDegraded` | warning | a bond member is down, or sits outside the LACP aggregator the bond uses (it has link and carries nothing) |
| `LinkSpeedLow` | warning | a physical link that is up runs below its expected speed |

A bond member's alert is on `<bond>/<member>`, the others on the interface. A port
that is down and not named in the policy raises nothing: machines carry unused
ports. The rule is the one of [storage alerts](storage.md#storage-health-alerts):
a failed read, a silent agent, or a report that no longer mentions a device
neither raises nor clears anything, and a link that is down keeps its open
`LinkSpeedLow`, having no speed to read.

A member that left a bond altogether is in no list, so only `members` in the
[`NetworkHealthPolicy`](crd-reference.md#networkhealthpolicy) can notice it. Error
and drop counters are reported and shown, not alerted on: they accumulate since
the driver loaded, and a threshold on a total says nothing about now.

Expectations are edited in Network → Links, through `frame-uiproxy`, and reach each
machine at its next pass.
