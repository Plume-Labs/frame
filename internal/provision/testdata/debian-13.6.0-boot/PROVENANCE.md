# Real Debian netinst boot configuration

Extracted on **2026-09-11** from `debian-13.6.0-amd64-netinst.iso`, SHA256
`65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7` — the pin
in `DefaultBase()`. Downloaded from
`https://cdimage.debian.org/cdimage/release/13.6.0/amd64/iso-cd/debian-13.6.0-amd64-netinst.iso`
(302-redirected to a mirror; the file that landed on disk was verified
against the pinned SHA256 above before anything was extracted from it).

```
xorriso -osirrox on -indev debian-13.6.0-amd64-netinst.iso \
  -extract /isolinux isolinux -extract /boot/grub grub
```

Every `.cfg` file under `/isolinux` and `/boot/grub` (recursive) in the real
image is committed here, unmodified, under `isolinux/` and `boot/grub/`. The
ISO itself (792 MB uncompressed on disk, ~755 MiB reported by `curl`) was
deleted immediately after extraction — this directory holds only the ~116 KiB
of `.cfg` text.

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

**There are eighteen `.cfg` files under `isolinux/`, not two.** Most
(`addrk.cfg`, `addrkgtk.cfg`, `adgtk.cfg`, `adspkgtk.cfg`, `adtxt.cfg`,
`drk.cfg`, `drkgtk.cfg`, `rqdrk.cfg`, `rqdrkgtk.cfg`, `rqgtk.cfg`,
`rqspkgtk.cfg`, `rqtxt.cfg`, `spkgtk.cfg`) carry their own `label`/`kernel`/
`append` triples for the accessible, dark-contrast, rescue, and speech-synth
variants of the same menu tree; a few (`drkmenu.cfg`, `stdmenu.cfg`,
`exithelp.cfg`, `prompt.cfg`) carry no kernel line at all and are menu
styling or submenu plumbing.

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
