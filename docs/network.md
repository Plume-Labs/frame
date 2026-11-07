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
| `NetworkDegraded` | critical | a bond is down, unless it has no member and the policy does not name it; a bond holds fewer members than `members`; a link named in the policy is down or absent |
| `NetworkDegraded` | warning | a bond member is down, or sits outside the LACP aggregator the bond uses (it has link and carries nothing) |
| `LinkSpeedLow` | warning | a physical link that is up, or at the kernel's `unknown`, runs below its expected speed |

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
and drop counters are reported and shown, not alerted on: they accumulate since
the driver loaded, and a threshold on a total says nothing about now.

Expectations are edited in Network → Links, through `frame-uiproxy`, and reach each
machine at its next pass.
