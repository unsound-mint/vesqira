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
- **What may be written is fixed before the request is read.** The folder comes from `VESQIRA_DERIVED`, set by the launcher. It must resolve inside `VESQIRA_ROOTS`, must not be a symbolic link, and is made (one component) when missing. The file name has a fixed shape (`mask-…` or `subset-…`, the op's own prefix), and the file must not exist. Options are an allow-list per operation, by exact name (argparse would also accept an abbreviation such as `--miss` for `--missing-out`); `--out` is added by `hostrun.py` itself.
- **Whole or not at all.** The analysis program writes to a private temporary name in that folder, which is `link()`ed to the final name only once the program succeeded. `link` never replaces an existing file, so two runs cannot clobber each other. The temporary file is removed on every path the program controls.
- **Nobody listening, nothing placed.** On real ssh a remote command usually keeps running after the client goes away (no tty, no hang-up). So just before the `link()` the program writes a line to the caller; when the connection is gone (Vesqira timed out, the VPN dropped) that write fails and the run stops there with no file. A connection lost in the last instant can still leave a complete file with no catalog row, and a run killed outright (SIGKILL, the node rebooting) can leave a `.part-…` file in the derived folder; both are inert and named so the researcher can tell them apart.
- **Roots are enforced on the host.** Every read goes through `srcopen` with `VESQIRA_ROOTS`: the data file, a noise map, an imported mask file.
- **Identity is the host's.** The API records the new file's SHA-256 from the host, by the transfers' hashing path (`hosts.StatPath(hash)`), and its size and modification time in nanoseconds from the host's worker. Every later freshness check compares those.

**Why not `hosts.WriteFile` (ADR 0039).** It sends the content from this computer on stdin and is bounded at 1 MiB, so a mask of a cube would cross the VPN, which is what this decision avoids. ADR 0039's rules are kept where the bytes are made instead:

- a fixed folder inside the roots, with no link on the way;
- a name Vesqira chose;
- never replacing a file;
- an atomic, complete-or-nothing write;
- an identity verified independently afterwards.

ADR 0039 gains a note saying so.

**Why on the login node, and not as a Slurm job.** Making a mask and writing a subset are each one bounded pass over the file, the same kind of read the host's worker already does for a region spectrum. Moment maps, alignment and convolution stay Slurm jobs. The pass runs in its own process (not the persistent worker, so the viewer is not blocked), under the analysis memory cap (`memcap`), the heavy-workload slot and the analysis timeout. A run that times out is not recorded, and the program on the host stops before placing a file once nobody is listening (above); the sentence says what to do instead (make it from a smaller window, or run the work as a job). A job would put a queue wait in front of a one-pass mask, and would need a draft–review–submit–register loop in the Masks section.

**Masks on a host (ADR 0055).**
- Making one: all methods work. The `missing`, `low_snr` (a stated σ, the MAD estimate or a noise map), `saturated` and `regions` masks are made the same way as here. An `import` (a mask file the researcher made) works too. A mask-role import rule in a host project makes the mask the same way (see the addendum). A noise map or a mask file must be on the same machine as the dataset (409, with the way out: transfer one, or register a copy on the file's host). Tiers, rules and provenance are unchanged. The row records the host (`masks.host_id`) and the mask's file is the host's. Forgetting a mask leaves its file on the host (ADR 0041: Vesqira deletes no data file).
- Freshness and identity: as here. A mask made from other bytes than the dataset has now is stale. A mask file whose size or modification time on the host changed is refused with "make it again". A mask on another machine than the file being read is refused (a dataset with a local and a host location reads one of them).
- Applying: the region spectrum and region histogram send the masks' host paths to the host's worker, which reads them next to the file. Statistics and moment maps run as analysis jobs on the host, so the job's script names the masks by path, type, policy and weight only, single-quoted JSON whose shape `RenderAnalysis` checks and which cannot carry a single quote. The result records the id-free, path-free snapshot, as a run here does. A job's masks are checked (fresh, unchanged on the host) when the job is drafted, not when it runs: the fingerprint recorded is the one as drafted, and a mask file changed on the host between drafting and running is not detected. Relative orientation is still computed on this computer only (kept, reworded below).
- Offline copies (ADR 0059) never stand in for a masked reply: a masked region request is keyed with its masks and their fingerprints, so it never matches an unmasked copy, and a mask's hatch plane is keyed by the mask, never by a dataset that has a copy, so preparing a copy never keeps one.
- The hatch: `GET /api/masks/{id}/plane` for a host mask checks the file on the host and asks the host's worker for one plane.

**Subsets written on a host (ADR 0063).** `POST /api/subsets/{sid}/materialise` for a host source runs the same `subset` operation through `hostrun.py`, with the window, the note, the declared unit and the corrections in force. The file is registered as a dataset on that host (the ordinary remote registration), with the same derived record and reuse key as here. A second request for the same version, bytes, corrections and unit reuses it. Viewing a host subset already went through the host's worker with the window echo check, and is now tested against a local copy.

**Corrections through a host (ADR 0061).**
- **Viewing:** unchanged in code. The correcting service already sent the state for the host path and required its echo. It is now tested: the readouts equal a local copy's, and a worker on the host that does not echo the hash is refused (502, "nothing is shown rather than uncorrected values").
- **Writing a subset or making a mask on a host:** the state goes with the request and its echo is required, as for a run here.
- **Analysis jobs on a host now receive the corrections**, instead of being refused. The states in force for the job's input, reference and reliability map go into the script as `--corrections '<json>'` (checked shape, no single quote). The job's program builds the WCS and unit with them, as here: the result's header carries the corrected values marked `VESQWCSC`, the summary's policy says so, and `result.json` echoes the hashes. The draft records the hashes and the id-free snapshot. Registering the result, or reading a statistics result, requires the echo (502 otherwise: "its result is not used, run the job again"). The result is pinned to the state in force when the job was drafted, and the derived record keeps that state. Every corrected value is a number or validated plain text (`catalog.ValidUnit`, `CheckKeyword`), so no quote can reach the script; `RenderAnalysis` refuses one anyway.

**Choice for host jobs: pass the corrections, not "materialise a corrected copy first".** The analysis program already applies a mapping and echoes it. The same mechanical guard that protects a run here (the echo, checked before anything is recorded) protects a job. A refusal would make the researcher write a full corrected copy of a multi-GB cube on the cluster for every corrected analysis: the copy this product avoids. A corrected copy stays possible (a subset written as a file on the host carries the corrected header) when the researcher wants one.

**Refusals removed:**
- making a mask of a host file;
- applying masks to a host file (region spectrum and histogram, statistics and moment jobs);
- the hatch plane of a host mask;
- writing a subset of a host file as a file, with or without carrying masks;
- a mask-role import rule in a project on a host;
- an analysis job on a dataset with corrections in force.

**Refusals kept, reworded to say what to do:**
- ~~Carrying masks into a result of a cutout, slab, align or convolve job on a host: 422.~~ Lifted: a subset file carries them (first addendum) and so do these jobs (second addendum).
- A noise map or mask file on another machine than the dataset: 409.
- Relative orientation of a map on a host: computed on this computer only; download the map with Transfers and compare the copy.
- A mask on another machine than the location being read (a dataset with a local and a host location): 409, make it again where the file is read.
- A mask while the file's host is unreachable and an offline copy answers: 503, since offline copies are unmasked. A mask on a virtual subset: 409, unchanged.

## Addendum: masks carried into a subset file on a host, and mask import rules on host projects

Both items were deferred above. Status: implemented, awaiting owner review.

**Carrying masks into a subset written on a host.** `POST /api/subsets/{sid}/materialise` accepts `carryMasks` for a host source with the rules of the local path (ADR 0055): nothing is carried unless asked, the no-value mask is never carried but computed again from the result, the other masks of the source are recorded as left behind, and a stale or changed source mask is refused before anything is written. The same `planCarry` and `recordCarried` code serves both paths; the plan only knows which machine it writes on (`carryPlan.hostID`).
- `hostrun.py` takes the extra outputs as structured request fields, `carry: [{name, path, type}]` and `missing: <name>`, never as options. `--carry-masks` and `--missing-out` stay out of the allow-list; the program builds them itself from private temporary names (`.part-<tag>-<name>`). Only a subset may carry (a mask run with them is refused); a name has the mask shape and is distinct from the others (at most 16 carried); the type is one of the carried types (never `missing`); the source path must be absolute and is read through `srcopen`, so the roots are enforced on it like every other read. No path to write can be chosen by the caller.
- All or nothing: every final name must be new before the program runs; after it, every temporary must be a plain file, and the "placing" line is written once, before any `link()`. The files are then linked one by one; if any link fails (a name that appeared meanwhile), the ones already linked in this run are removed and nothing stays. A connection lost between two links, or a run killed there (SIGKILL, the node rebooting), can leave a subset of the files placed with no catalog row, inert and named like the others; only a failure the program can see is rolled back.
- The reply names the resolved file of each output (`carried_files`, `missing_file`); the API requires each to be the name asked for, in the same folder as the result, and refuses the whole result (502) otherwise. Each mask is registered with `host_id` set; its SHA-256, size and modification time are read on the host as for any host file. A mask that cannot be recorded (the host went away) leaves the written files on the host, inert, and the API answers with an error and records no mask; Vesqira deletes nothing on a host, so `carryPlan.cleanup` does not remove a host's files (it would otherwise call `os.Remove` on a path of another machine).
- Cutout, slab, align and convolve on a host are Slurm jobs that write their own result; carrying through them is the next addendum.

**Mask-role import rules in a project on a host (ADR 0058).** The skip was a guard written before the mask route worked on hosts, not a protection: `maskByRule` goes through `makeMask`, which already makes the mask on the host through `hostrun.py` (same-machine check of the mask file and the dataset, roots on the host, the allow-listed options, never replacing a file). The guard is removed; there is no new write path. A target not registered, another grid, a point catalog and an unreachable host behave as on this computer (the last is `failed`, so a later apply tries again, and records no mask). Applying a rule over many mask files is one pass on the login node per mask, serial, under the same analysis timeout and heavy-workload slot as a mask made by hand.

**Validation (addendum).**
- `services/scientific-worker/test_hostrun.py`: a subset writes the carried masks and the recomputed no-value mask together; a failing carried mask and a source mask outside the roots leave nothing; a failing `link()` of the third output removes the two placed; a colliding name (and a duplicate name in one request) refuses before anything is made; `--carry-masks`, `--missing-out`, abbreviations, bad names, the `missing` type, a relative path, a caller-chosen `out`, more than 16 masks and a carry on a mask run are refused.
- `services/api/internal/server/hostcarry_test.go`: a subset of a host file written with a carried mask has, on the host, the same masks as the same subset of a local copy (counts, shape, tier, method, name, hatch plane byte-equal), recorded with `host_id`, the carried fingerprint equals the host file's SHA-256, the derived record has the same carry account and names no path, the same request is reused and carrying nothing is another file, and a changed source mask is refused with no file written. `hostparity_test.go` no longer expects the 409.
- `services/api/internal/server/projects_import_mask_host_test.go`: a mask rule in a host project makes a declared mask on the host (author `rule:<name>`, `host_id` set, file in the host's derived folder, none here, both source files unchanged), a missing target is retried, another grid is refused, applying twice makes one mask, and an unreachable host records none.
- `apps/web/src/catalog/subsets.test.tsx`: a host source offers the carry choice and sends it.
- Not verified: a real cluster (NFS `link()`, a login node's time for several outputs); a connection lost between two links; a moment or other job with carried masks (not built).

## Addendum: masks carried through cutout, slab, align and convolve jobs on a host

Status: implemented, awaiting owner review. The jobs write their own result, so the carry is split in two: the script writes the files, registering the result records them.

- **Drafting.** `POST /api/analysis/job` takes `carryMasks` with the rules of ADR 0055 (`planCarry`, the plan the local path and the subset use): nothing is carried unless asked, the no-value mask is never carried but computed again, align and convolve want every mask decided, and a mask the file does not have (422), a stale one or one on another machine (409) is refused before a job exists. The script gets `--carry-masks '<json>'` and `--missing-out <file>`. The files are fixed names the API generates, `mask-carried-<dataset>-<type>-<hex>.fits` and `mask-missing-<dataset>-missing-<hex>.fits`, directly in the job's own output folder. `RenderAnalysis` refuses any other folder or name, a free-text key, a quote, the `missing` type among the carried ones, a repeated file, and carrying on any other analysis. Nothing about them comes from the browser.
- **The job's record.** The job's params keep `carryPlan`: the ids of the source masks, the SHA-256 each had at drafting, the planned files, the masks left behind, and the reference named for an alignment's rule sentence. The drafted script is the one submitted, so a source mask that changes before registering is detected by the fingerprint.
- **Registering.** After the result registers (corrections echo required first, as for any host job), the API resolves each planned file on the host through its worker (roots enforced there) and requires each to exist, to carry the planned name and to be in the folder of `result.fits`. It then re-checks each source mask (`usableMask`: same dataset, not stale, file unchanged on the host, same fingerprint as at drafting) and records the masks through `recordCarried` with `host_id` set, as the subset path does after `hostrun.py`. The result's derived record gets `maskCarry` like a local result (no ids, no paths).
- **Failure keeps the result, drops the masks.** If a planned file is missing or elsewhere, a source mask changed, or the program's summary does not account for the masks, no mask is recorded and the response carries `masksNote`, one sentence ending with "Make the masks again on the result." (shown under the job's buttons). A mask that was not written is never recorded. An unreachable host during the check is a 502 and decides nothing: registering again repeats it. If recording itself fails midway (the host went away between two files), masks recorded so far stay and the note says the rest could not be.
- **Registering twice** records the masks once: when a mask of the result already has one of the planned paths, the earlier `maskCarry` is kept.
- **Nothing is deleted on a host.** A job that failed leaves its folder; `carryPlan.cleanup` does not touch a host.
- **Same rules as local.** The mask's own flags follow the data by the same functions (`carry_after` in `masks.py`) because the job runs `analysis.py` itself.
- **Not covered:** a mask file written by the job that was altered on the host after the job ended and before registering is recorded with the fingerprint read at registering (as any host file); a job drafted before this change has no `carryPlan` and registers as before.
- **Validation.** `jobs/jobs_carry_test.go` (the script carries the options with the planned names; every other folder, name, key and op is refused). `server/hostjobcarry_test.go`: a cutout job with a carried mask registers masks equal to the same cutout of a local copy (counts, shape, tier, method, name), with `host_id`, hatch readable by the host's worker, a record without paths, nothing written here, a second registration recording once; a slab job whose carried file was removed registers the result with no mask and the note; a mask of another dataset, the no-value mask, a moment job and an undecided convolution are refused. `maskCarry.test.tsx`: the host form offers the choice and sends it.
- **Not verified:** a real cluster (NFS visibility of the files at registering, a compute node's umask), and align and convolve jobs with carried masks on a host (the same code path; tested with cutout and slab).

## Alternatives

- **Write masks through `hosts.WriteFile`.** Rejected: the bytes would cross the VPN and the 1 MiB bound would refuse most cubes.
- **Let the persistent host worker write.** Rejected: it would block the viewer of that host for the length of the pass, and it widens the one process the roots protect for every request, not only these two.
- **Make masks and subset files as Slurm jobs.** Rejected for now: a queue wait and a review loop for a single pass. It remains the path for analyses that compute per pixel from many (moments, alignment, convolution).
- **Keep the job refusal for corrected datasets, with "materialise a corrected copy first".** Rejected (above).
- **Copy the cube here.** Rejected: it is the cost the feature exists to avoid.

## Consequences

- The researcher's host folders gain `vesqira_derived/mask-…fits` and `vesqira_derived/subset-…fits` next to the analysis jobs' folders. Files are made with the account's umask; nothing is chmod-ed. They are inert to other programs and are never deleted by Vesqira.
- A run whose connection drops after the file was put in place but before the answer arrived leaves a complete file with no catalog row. It is named like the others, and a new request makes a new file.
- A dataset with both a local and a host location has its masks on the machine they were made on. Applying them while the other location is read is refused with "make it again where the file is read".
- `masks.path` stays `UNIQUE` across machines. Names carry a random part, so a clash between a local and a host path is not expected, and an insert that clashed would fail rather than mix files.
- A root reached through a symbolic link resolves on the host to its real path, while the API's remote checks (`InRoots`, `StatPath`) are lexical against the declared root. Not tried here: if registration works through such a root, reading a new mask's identity may fail and leave its file unrecorded.

## Validation

- `services/api/internal/server/hostparity_test.go`, with the real worker deployed through a fake ssh into a stand-in host and Slurm stand-ins (the test runs the job scripts as the cluster would):
  - **Masks.** Each method (missing, low S/N by MAD, saturated, regions, an imported file) made on the host equals the same mask made on a local copy: counts, shape, tier, method and rule. The files are on the host only, no temporary file is left, and the recorded SHA-256 is the host file's.
  - **Hatch, spectrum, histogram.** Hatch planes are byte-equal. Region spectra are byte-equal and region histograms equal (only the mask id differs) with exclude and keep policies.
  - **Statistics job with masks.** The script names masks by path, type and policy only. The summary equals the local run's.
  - **Refusals, masks.** A noise map or mask file on another machine (409); carrying in a host job (422); a host mask file that changed (409 for the spectrum and the hatch); an unreachable host (502, no row, no file). A mask name with quotes, `$(…)`, `;`, a backslash and `!` reaches the host as text: no command runs.
  - **Subsets.** View, planes, coordinate and spectrum through the host's worker equal a local copy's. The file written on the host equals the local one plane by plane, has the same derived record (with the mask left behind recorded) and is reused for the same version. Carrying is refused (409). A note with quotes and `$(…)` runs nothing.
  - **Reviewer.** A published figure of the host's file names no host path and not the derived folder, and the review server answers none of the mask, subset or masks routes.
  - **Corrections.** The view and coordinates of a corrected host file equal a corrected local copy's. A moment job of a corrected host file carries `--corrections`, registers with the corrected WCS (equal plane and coordinates to the local run), `VESQWCSC` in the header and the corrections in the derived record. A job whose `result.json` lacks the echo is refused at registration (502). A subset written on the host carries the corrections. A worker on the host made "older" (its echo removed, redeployed in place) is refused (502).
- `hostmasks_offline_test.go`: a masked spectrum of a host file is never answered from the unmasked offline copy, and a mask's plane is never served or kept as a copy. `hostparity_refusals_test.go`: a mask on another machine than the file read, and relative orientation of a host map, are refused with the way out.
- `services/api/internal/jobs/jobs_hostparity_test.go`: the script carries masks and corrections single-quoted, and refuses a quote, a newline, a free-text key, a bad path, a file the job does not read and a missing hash.
- `services/scientific-worker/test_hostrun.py`: one new file in the derived folder. It never replaces a file, refuses other writes, names, ops and inputs outside the roots (leaving nothing behind), and refuses a derived folder outside the roots, a linked one, and a missing `VESQIRA_ROOTS`.
- `services/api/internal/schema/maskshost_test.go` (schema 25 upgrades with masks on this computer) and `testdata/schema_v26.sql`.
- The web forms: `masksPanel.test.tsx` (a host file offers the mask form and says where masks live).
- **Not verified:**
  - a real cluster: login-shell banners, NFS `link()` semantics, astropy versions on the host, timings of a mask pass over a multi-GB cube on a login node;
  - a moment job with masks (the statistics job is tested; the same script path);
  - that a real sshd fails the "placing" write once the client is gone (the fake ssh ends the program with the client);
  - a root reached through a symbolic link;
  - the new notes at the narrow and compact laptop sizes.

## Rollback / Migration

- **Schema 26** adds `masks.host_id INTEGER NOT NULL DEFAULT 0`: every existing mask is on this computer. An upgrade copies the catalog aside first, and an older build refuses a version 26 catalog (ADR 0020).
- **Code rollback:** restore the refusals (`makeMask`, `regionMasks`, `maskPlane`, `materialiseSubset`, the job's masks and corrections checks) and drop `hostrun.py` from DEPLOY. Files already written on hosts stay and are inert; rows with a `host_id` would then be refused as being on another machine.
