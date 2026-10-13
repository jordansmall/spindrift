You never post the verdict comment yourself here: the launcher posts your
relayed verdict host-side once you exit, so you never learn its URL and must
always use `landing=none`. Once `driver-exec signal comment` exits zero
(`signal comment accepted: ...`), the verdict is relayed: report it in
`status` as usual; a relayed verdict is a delivered verdict, never
`blocked`. The relay failed only if it never exited zero, even after you
fixed each rejection's reason and sent it again.
