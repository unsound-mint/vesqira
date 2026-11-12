# ADR 0064: Masks, subset files and corrections for files on a host

- Status: proposed; implemented, awaiting owner review.
- Date: 2026-11-12
- Brings typed masks (ADR 0055), virtual subsets written as files (ADR 0063) and WCS/unit corrections (ADR 0061) to files that live on a host. Builds on the host's worker and its roots (ADR 0029), the host's derived folder that analysis jobs already use, the hashing path of transfers (ADR 0051), the write rules of ADR 0039 and the analysis jobs. Schema 26: `masks.host_id`. Expect renumbering at merge if another branch took 26.

## Context

The owner's H I cubes stay on a Slurm cluster reached over ssh through a VPN that drops. Three recent features worked only for files read on this computer:

- a mask of a host file was refused (409 "a mask is made on this computer");
- a subset of a host file could be viewed but not written as a file;
- an analysis job on a host was refused for a dataset with corrections in force.

The obvious workaround, copying the cube here, is the one the product exists to avoid (AGENTS.md: never move a whole cube).

## Decision

**Where the bytes are made.** A mask of a host file, and a subset of one written as a file, are made on the host. The host's own copy of the analysis program reads the file there and writes the result in the host's derived folder, `<first source root>/vesqira_derived`, the folder analysis jobs already write into. The only things that cross the VPN are:

- the program's summary (counts, rule, shape);
- a mask's hatch, one uint8 plane at a time, read by the host's worker for the screen.

`hostrun.py` (shipped in DEPLOY) is the one entry point, run over the user's own ssh with `hosts.Manager.HostRun`:

- **The request travels on stdin, never on the command line.** A mask's name, a threshold's source, a subset's reason and a corrected unit are free text, and the command line goes through the account's login shell. The command line itself is fixed: the deployed folder, the host's roots and derived folder (both from the host's saved settings), and the host's Python.
- **What may be written is fixed before the request is read.** The folder comes from `VESQIRA_DERIVED`, set by the launcher. It must resolve inside `VESQIRA_ROOTS`, must not be a symbolic link, and is made (one component) when missing. The file name has a fixed shape (`mask-…` or `subset-…`, the op's own prefix), and the file must not exist. Options that would write anywhere else (`--out`, `--carry-masks`, `--missing-out`, `--rel-out`, `--table-out`) are refused.
- **Whole or not at all.** The analysis program writes to a private temporary name in that folder, which is `link()`ed to the final name only once the program succeeded. `link` never replaces an existing file, so two runs cannot clobber each other. The temporary file is removed on every path. A dropped connection leaves no file or a complete one, never half of one.
- **Roots are enforced on the host.** Every read goes through `srcopen` with `VESQIRA_ROOTS`: the data file, a noise map, an imported mask file.
- **Identity is the host's.** The API records the new file's SHA-256 from the host, by the transfers' hashing path (`hosts.StatPath(hash)`), and its size and modification time in nanoseconds from the host's worker. Every later freshness check compares those.

**Why not `hosts.WriteFile` (ADR 0039).** It sends the content from this computer on stdin and is bounded at 1 MiB, so a mask of a cube would cross the VPN, which is what this decision avoids. ADR 0039's rules are kept where the bytes are made instead:

- a fixed folder inside the roots, with no link on the way;
- a name Vesqira chose;
- never replacing a file;
- an atomic, complete-or-nothing write;
- an identity verified independently afterwards.

ADR 0039 gains a note saying so.

**Why on the login node, and not as a Slurm job.** Making a mask and writing a subset are each one bounded pass over the file, the same kind of read the host's worker already does for a region spectrum. Moment maps, alignment and convolution stay Slurm jobs. The pass runs in its own process (not the persistent worker, so the viewer is not blocked), under the analysis memory cap (`memcap`), the heavy-workload slot and the analysis timeout. A run that times out is stopped and nothing is kept; the sentence says what to do instead (make it from a smaller window, or run the work as a job). A job would put a queue wait in front of a one-pass mask, and would need a draft–review–submit–register loop in the Masks section.

**Masks on a host (ADR 0055).**
- Making one: all methods work. The `missing`, `low_snr` (a stated σ, the MAD estimate or a noise map), `saturated` and `regions` masks are made the same way as here. An `import` (a mask file the researcher made) works too, and so does a mask-role import rule, whose host skip is removed. A noise map or a mask file must be on the same machine as the dataset (409, with the way out: transfer one, or register a copy on the file's host). Tiers, rules and provenance are unchanged. The row records the host (`masks.host_id`) and the mask's file is the host's. Forgetting a mask leaves its file on the host (ADR 0041: Vesqira deletes no data file).
- Freshness and identity: as here. A mask made from other bytes than the dataset has now is stale. A mask file whose size or modification time on the host changed is refused with "make it again". A mask on another machine than the file being read is refused (a dataset with a local and a host location reads one of them).
- Applying: the region spectrum and region histogram send the masks' host paths to the host's worker, which reads them next to the file. Statistics and moment maps run as analysis jobs on the host, so the job's script names the masks by path, type, policy and weight only, single-quoted JSON whose shape `RenderAnalysis` checks and which cannot carry a single quote. The result records the id-free, path-free snapshot, as a run here does. Relative orientation is still computed on this computer only (kept, reworded below).
- The hatch: `GET /api/masks/{id}/plane` for a host mask checks the file on the host and asks the host's worker for one plane.

**Subsets written on a host (ADR 0063).** `POST /api/subsets/{sid}/materialise` for a host source runs the same `subset` operation through `hostrun.py`, with the window, the note, the declared unit and the corrections in force. The file is registered as a dataset on that host (the ordinary remote registration), with the same derived record and reuse key as here. A second request for the same version, bytes, corrections and unit reuses it. Viewing a host subset already went through the host's worker with the window echo check, and is now tested against a local copy.

**Corrections through a host (ADR 0061).**
- **Viewing:** unchanged in code. The correcting service already sent the state for the host path and required its echo. It is now tested: the readouts equal a local copy's, and a worker on the host that does not echo the hash is refused (502, "nothing is shown rather than uncorrected values").
- **Writing a subset or making a mask on a host:** the state goes with the request and its echo is required, as for a run here.
- **Analysis jobs on a host now receive the corrections**, instead of being refused. The states in force for the job's input, reference and reliability map go into the script as `--corrections '<json>'` (checked shape, no single quote). The job's program builds the WCS and unit with them, as here: the result's header carries the corrected values marked `VESQWCSC`, the summary's policy says so, and `result.json` echoes the hashes. The draft records the hashes and the id-free snapshot. Registering the result, or reading a statistics result, requires the echo (502 otherwise: "its result is not used, run the job again"). The result is pinned to the state in force when the job was drafted, and the derived record keeps that state. A corrected value containing a quote is refused with the way out: write a subset of it as a file on the host, which carries the corrected header, and analyse that.

**Choice for host jobs: pass the corrections, not "materialise a corrected copy first".** The analysis program already applies a mapping and echoes it. The same mechanical guard that protects a run here (the echo, checked before anything is recorded) protects a job. A refusal would make the researcher write a full corrected copy of a multi-GB cube on the cluster for every corrected analysis: the copy this product avoids. The quote refusal keeps the copy as the way out for the one case the script cannot carry.

**Refusals removed:**
- making a mask of a host file;
- applying masks to a host file (region spectrum and histogram, statistics and moment jobs);
- the hatch plane of a host mask;
- a mask-role import rule on a host project;
- writing a subset of a host file as a file;
- an analysis job on a dataset with corrections in force.

**Refusals kept, reworded to say what to do:**
- Carrying masks into a result written on a host (a cutout, slab, align or convolve job, or a subset file): 422/409, "run it without carrying them (leave them behind), then make the masks again on the result, on the host". A subset file on a host records the source's masks as left behind, the no-value mask included (it is not computed again there).
- A noise map or mask file on another machine than the dataset: 409.
- Relative orientation of a map on a host: computed on this computer only; download the map with Transfers and compare the copy.
- A corrected value with a quote in a job: 409; write a subset as a file and analyse it.
- A mask while the file's host is unreachable and an offline copy answers: 503, since offline copies are unmasked. A mask on a virtual subset: 409, unchanged.

## Alternatives

- **Write masks through `hosts.WriteFile`.** Rejected: the bytes would cross the VPN and the 1 MiB bound would refuse most cubes.
- **Let the persistent host worker write.** Rejected: it would block the viewer of that host for the length of the pass, and it widens the one process the roots protect for every request, not only these two.
- **Make masks and subset files as Slurm jobs.** Rejected for now: a queue wait and a review loop for a single pass. It remains the path for analyses that compute per pixel from many (moments, alignment, convolution).
- **Keep the job refusal for corrected datasets, with "materialise a corrected copy first".** Rejected as the default (above); kept as the way out for values a script cannot carry.
- **Copy the cube here.** Rejected: it is the cost the feature exists to avoid.

## Consequences

- The researcher's host folders gain `vesqira_derived/mask-…fits` and `vesqira_derived/subset-…fits` next to the analysis jobs' folders. Files are made with the account's umask; nothing is chmod-ed. They are inert to other programs and are never deleted by Vesqira.
- A run whose connection drops after the file was put in place but before the answer arrived leaves a complete file with no catalog row. It is named like the others, and a new request makes a new file.
- A dataset with both a local and a host location has its masks on the machine they were made on. Applying them while the other location is read is refused with "make it again where the file is read".
- `masks.path` stays `UNIQUE` across machines. Names carry a random part, so a clash between a local and a host path is not expected, and an insert that clashed would fail rather than mix files.
- A root reached through a symbolic link resolves on the host to its real path, and the existing remote checks are lexical against the declared root. Such a root is no more supported for masks than it was for registration (not changed here).

## Validation

- `services/api/internal/server/hostparity_test.go`, with the real worker deployed through a fake ssh into a stand-in host and Slurm stand-ins (the test runs the job scripts as the cluster would):
  - **Masks.** Each method (missing, low S/N by MAD, saturated, regions, an imported file) made on the host equals the same mask made on a local copy: counts, shape, tier, method and rule. The files are on the host only, no temporary file is left, and the recorded SHA-256 is the host file's.
  - **Hatch, spectrum, histogram.** Hatch planes are byte-equal. Region spectra are byte-equal and region histograms equal (only the mask id differs) with exclude and keep policies.
  - **Statistics job with masks.** The script names masks by path, type and policy only. The summary equals the local run's.
  - **Refusals, masks.** A noise map or mask file on another machine (409); carrying in a host job (422); a host mask file that changed (409 for the spectrum and the hatch); an unreachable host (502, no row, no file). A mask name with quotes, `$(…)`, `;`, a backslash and `!` reaches the host as text: no command runs.
  - **Subsets.** View, planes, coordinate and spectrum through the host's worker equal a local copy's. The file written on the host equals the local one plane by plane, has the same derived record (with the mask left behind recorded) and is reused for the same version. Carrying is refused (409). A note with quotes and `$(…)` runs nothing.
  - **Reviewer.** A published figure of the host's file names no host path and not the derived folder, and the review server answers none of the mask, subset or masks routes.
  - **Corrections.** The view and coordinates of a corrected host file equal a corrected local copy's. A moment job of a corrected host file carries `--corrections`, registers with the corrected WCS (equal plane and coordinates to the local run), `VESQWCSC` in the header and the corrections in the derived record. A job whose `result.json` lacks the echo is refused at registration (502). A subset written on the host carries the corrections. A worker on the host made "older" (its echo removed, redeployed in place) is refused (502).
- `services/api/internal/jobs/jobs_hostparity_test.go`: the script carries masks and corrections single-quoted, and refuses a quote, a newline, a free-text key, a bad path, a file the job does not read and a missing hash.
- `services/scientific-worker/test_hostrun.py`: one new file in the derived folder. It never replaces a file, refuses other writes, names, ops and inputs outside the roots (leaving nothing behind), and refuses a derived folder outside the roots, a linked one, and a missing `VESQIRA_ROOTS`.
- `services/api/internal/schema/maskshost_test.go` (schema 25 upgrades with masks on this computer) and `testdata/schema_v26.sql`.
- The web forms: `masksPanel.test.tsx` (a host file offers the mask form and says where masks live).
- **Not verified:**
  - a real cluster: login-shell banners, NFS `link()` semantics, astropy versions on the host, timings of a mask pass over a multi-GB cube on a login node;
  - a moment job with masks (the statistics job is tested; the same script path);
  - a mask-role import rule on a host project;
  - a root reached through a symbolic link;
  - the new notes at the narrow and compact laptop sizes.

## Rollback / Migration

- **Schema 26** adds `masks.host_id INTEGER NOT NULL DEFAULT 0`: every existing mask is on this computer. An upgrade copies the catalog aside first, and an older build refuses a version 26 catalog (ADR 0020).
- **Code rollback:** restore the three refusals (`makeMask`, `regionMasks`, `materialiseSubset`, the job corrections check) and drop `hostrun.py` from DEPLOY. Files already written on hosts stay and are inert; rows with a `host_id` would then be refused as being on another machine.
