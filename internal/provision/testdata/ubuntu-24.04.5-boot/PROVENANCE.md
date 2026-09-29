# Provenance

`boot/grub/grub.cfg` and `boot/grub/loopback.cfg` are copied byte for byte
from `ubuntu-24.04.5-live-server-amd64.iso` (4,080,486,400 bytes, SHA256
`97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8`, as listed
in https://releases.ubuntu.com/24.04/SHA256SUMS), extracted on 2026-09-29 with:

    xorriso -osirrox on -indev ubuntu-24.04.5-live-server-amd64.iso \
      -extract /boot/grub/<file> <file>

These are the only files on that image carrying kernel lines. The image has no
`isolinux/`: GRUB serves both firmware modes. Its two El Torito entries, as
`xorriso -report_el_torito plain` lists them:

    El Torito boot img :   1  BIOS  y   none  0x0000  0x00      4        1197
    El Torito boot img :   2  UEFI  y   none  0x0000  0x00  10160     1989719

SHA256 of the extracted files:

    c41bfc7a3756df49e438b736dab17cc6f17114564178223a673ed310c07781be  grub.cfg
    60c5fcb9cd372b5fd27e8b5e2e27c9f1c340dc33ed20442f2dd05ab7fda5f4a4  loopback.cfg
