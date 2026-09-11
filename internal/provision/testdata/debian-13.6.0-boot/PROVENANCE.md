# Real Debian netinst boot configuration

Extracted on **2026-09-11** from `debian-13.6.0-amd64-netinst.iso`, SHA256
`65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7` — the pin
in `DefaultBase()`. Downloaded from
`https://cdimage.debian.org/cdimage/release/13.6.0/amd64/iso-cd/debian-13.6.0-amd64-netinst.iso`
(302-redirected to a mirror; the file that landed on disk was verified with
`sha256sum` against the pinned SHA256 above before anything was extracted
from it). Re-downloaded and re-verified a second time on **2026-09-11**
(same day, second pass) to produce the definitive listing below; the
checksum matched exactly both times.

```
xorriso -osirrox on -indev debian-13.6.0-amd64-netinst.iso \
  -extract /isolinux isolinux -extract /boot/grub grub
```

## The definitive listing

An earlier version of this document asserted "every `.cfg` file under
`/isolinux` and `/boot/grub` is committed here" and separately claimed
"eighteen `.cfg` files under `isolinux/`" while naming seventeen — neither
number came from anything that was actually counted against the image. This
section replaces that prose with the command output itself.

**`xorriso -indev debian-13.6.0-amd64-netinst.iso -ls /isolinux/`** (21
`.cfg` entries, all committed under `isolinux/` here; the rest are boot
binaries, `.c32` modules, F-key help text, and `splash.png`, none of which
this directory carries):
```
'addrk.cfg'
'addrkgtk.cfg'
'adgtk.cfg'
'adspkgtk.cfg'
'adtxt.cfg'
'boot.cat'
'drk.cfg'
'drkgtk.cfg'
'drkmenu.cfg'
'exithelp.cfg'
'f1.txt'
'f10.txt'
'f2.txt'
'f3.txt'
'f4.txt'
'f5.txt'
'f6.txt'
'f7.txt'
'f8.txt'
'f9.txt'
'gtk.cfg'
'isolinux.bin'
'isolinux.cfg'
'ldlinux.c32'
'libcom32.c32'
'libutil.c32'
'menu.cfg'
'prompt.cfg'
'rqdrk.cfg'
'rqdrkgtk.cfg'
'rqgtk.cfg'
'rqspkgtk.cfg'
'rqtxt.cfg'
'spkgtk.cfg'
'splash.png'
'stdmenu.cfg'
'txt.cfg'
'vesamenu.c32'
'win32-loader.ini'
```
Every one of the 21 `.cfg` names above (`addrk.cfg` through `txt.cfg`) is
committed under `isolinux/` in this directory — verified with `diff` between
this listing (filtered to `*.cfg`, sorted) and `ls isolinux/*.cfg` (sorted):
identical, both 21 entries. **Nothing was dropped.** The earlier "eighteen"
figure was simply a wrong prose count, not evidence of a gap.

**`xorriso -indev debian-13.6.0-amd64-netinst.iso -ls /boot/grub/`**:
```
'efi.img'
'font.pf2'
'grub.cfg'
'theme'
'x86_64-efi'
```

**`xorriso -indev debian-13.6.0-amd64-netinst.iso -ls /boot/grub/x86_64-efi/`**:
```
'grub.cfg'
```

`theme/` (checked with `-lsl`) holds ten unsuffixed theme description files
(`1`, `1-1`, `1-1-1`, `1-2`, `1-2-1`, `dark-1`, `dark-1-1`, `dark-1-1-1`,
`dark-1-2`, `dark-1-2-1`) and `hl_c.png` — no `.cfg` files. So `boot/grub/`
carries exactly two `.cfg` files on the real image, `grub.cfg` and
`x86_64-efi/grub.cfg`, and both are committed here.

**Total: every `.cfg` file under `/isolinux` and `/boot/grub` on the real
image (23 files) is committed in this directory.** This is now a verified
claim, backed by the listings above, not a count.

## The four names `menu.cfg` includes that are not on this image

`isolinux/menu.cfg` (committed here) contains:
```
include adspk.cfg
...
include spk.cfg
...
include x86drkme.cfg
...
include x86menu.cfg
```
None of `adspk.cfg`, `spk.cfg`, `x86drkme.cfg`, `x86menu.cfg` appear in the
`-ls /isolinux/` listing above. Searched the entire image, not just
`/isolinux/`, in case they landed somewhere else:
```
$ xorriso -indev debian-13.6.0-amd64-netinst.iso -find / \
    -name 'adspk.cfg' -or -name 'spk.cfg' -or -name 'x86drkme.cfg' -or -name 'x86menu.cfg'
```
produced zero matches. **These four files do not exist anywhere on this
image.** This is stated as a fact about the image, verified by `-find`, not
inferred from their absence in one directory listing.

Two of the four (`spk.cfg`, `adspk.cfg`) are the speech-synthesis submenu
entries; `x86menu.cfg`/`x86drkme.cfg` read as architecture-specific includes
(the "x86" prefix, alongside a same-named `x86_64-efi` directory elsewhere on
the image, suggests a per-architecture split that this particular netinst
build doesn't populate for BIOS boot). **syslinux/isolinux silently ignores
an `include` directive naming a file that isn't present** — this is
documented syslinux behavior, not something this task verified by execution
— so a missing include is consistent with a working, unattended boot: the
menu simply doesn't offer those entries, and everything else in `menu.cfg`
still loads. Whether Debian's build process for this particular netinst
release intentionally omits speech-synthesis support, or whether it's
present on a different architecture's image, was not investigated — it is
out of scope for what this directory exists to prove (the shape of the boot
config `Remaster` rewrites), and is recorded here rather than left for the
next reader to wonder about, since a reviewer already did.

## Why this directory exists at all

Task 2's original `iso_test.go` built its own two-file fixture
(`isolinux/txt.cfg`, `boot/grub/grub.cfg`) with an invented `default install`
line as the first line of `txt.cfg`. **That line does not exist in the real
image.** The invented fixture made a two-file, two-line-format rewrite look
sufficient; it was not. The gaps below were found only by extracting the real
files and reading them, which is why this directory — not another synthetic
fixture — is now the basis for every test that asserts what a real remaster
produces. `tinyBaseISO` (the original synthetic fixture) is kept for the
handful of cases that need a deliberately degenerate image: no kernel line
anywhere, or only one of the two boot config trees present.

## What the real files show that the invented fixture hid

**The BIOS entry that actually boots unattended is `isolinux/gtk.cfg`, not
`isolinux/txt.cfg`.** `isolinux.cfg` sets `prompt 0` and `timeout 0` and
boots `vesamenu.c32`, which includes `menu.cfg`, which includes `gtk.cfg`
*before* `txt.cfg`. `gtk.cfg` carries `default installgui` and `menu default`
on its one entry; `txt.cfg` carries **no `default` directive of any kind**.
With `prompt 0`/`timeout 0`, the menu never waits for a keypress — it boots
whichever entry is marked default, which is the graphical installer in
`gtk.cfg`. A rewrite that only ever opened `txt.cfg` left the actual boot
path with none of the unattended arguments: on real hardware the machine
starts the graphical installer and sits at a language-selection prompt
forever, reachable only by its BMC.

**There are twenty-one `.cfg` files under `isolinux/`, not two** (see the
definitive listing above). Most
(`addrk.cfg`, `addrkgtk.cfg`, `adgtk.cfg`, `adspkgtk.cfg`, `adtxt.cfg`,
`drk.cfg`, `drkgtk.cfg`, `rqdrk.cfg`, `rqdrkgtk.cfg`, `rqgtk.cfg`,
`rqspkgtk.cfg`, `rqtxt.cfg`, `spkgtk.cfg`) carry their own `label`/`kernel`/
`append` triples for the accessible, dark-contrast, rescue, and speech-synth
variants of the same menu tree; a few (`drkmenu.cfg`, `stdmenu.cfg`,
`exithelp.cfg`, `prompt.cfg`, `isolinux.cfg`, `menu.cfg`) carry no kernel
line at all and are menu styling, top-level boot config, or submenu
plumbing.

**`boot/grub/grub.cfg` already ships five entries with `auto=true
priority=critical`.** Debian's own "Automated install" family (BIOS and
UEFI, plain/dark/speech variants) sets these two tokens without a `file=`
directive, because on real hardware `auto=true` alone still prompts for the
preseed location. A rewrite that treated "already has `auto=true`" as "we
already wrote this" skips all five, permanently: they never receive
`file=/cdrom/preseed.cfg`, and are boot-worthy submenu entries carrying
`auto=true priority=critical` with no source of a preseed file. Confirmed by
`grep -c 'auto=true priority=critical' grub.cfg` → `5`.

**`boot/grub/x86_64-efi/grub.cfg`** is a one-line `source /boot/grub/grub.cfg`
wrapper — no kernel line, included for completeness since it lives under
`boot/grub/`.

## What is not here

Everything else on the real ISO — kernel/initrd binaries, `.c32` modules,
themes, `splash.png`, `isolinux.bin`, `boot.cat`, `efi.img`, `f1.txt`
through `f10.txt` help screens — was not extracted. This directory holds
only the text the unattended-boot rewrite reads and writes.

One `.cfg` file exists on the real image outside the two directories this
task scoped: `/EFI/debian/grub.cfg`. Not extracted — out of scope per the
coordinator's instruction (`/isolinux` and `/boot/grub` only) — noted here so
a future reader doesn't have to rediscover it if the EFI boot path ever needs
the same treatment.
