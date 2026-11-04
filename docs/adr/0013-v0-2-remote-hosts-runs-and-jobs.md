# ADR 0013: v0.2 remote hosts, runs and Slurm jobs

- Status: accepted under delegated merge, 2026-11-04; owner review pending
- Date: 2026-11-04

## Context
v0.1 works on files under local source roots. The next step is the researcher's real workflow: results produced on an HPC cluster by Slurm jobs (VGT, ROHSA and layer cubes), cubes that live on another laptop, comparison of all of these with cubes on the local machine, standard cube statistics and coordinate alignment, and a workspace in which runs, files and boards are linked for a supervisor.

## Decisions (from the owner)
- **Another laptop means a second source of cubes (a remote host), not a second user of the app.** It is a host like the cluster login node, reached the same way, and several hosts can be connected at once.
- **Connectivity is the user's own.** A VPN (for example gpclient) and `ssh` are managed outside Vesqira. Vesqira runs the system `ssh` with the user's own config and agent (connection multiplexing allowed). It never stores, reads or transmits keys or passwords, and never opens network access to the owner API.
- **Approval policy for Slurm.** Actions requested from the owner's own machine through the loopback API need no extra confirmation: the owner is the authority there, and the exact script is still shown before submit. Any action requested by another principal (a reviewer link, another user, any future non-loopback client) needs the owner's explicit approval, enforced on the server against a hash of the exact script. No such principal exists yet, so the server refuses every non-owner submission until the auth design lands.
- **Analysis scope.** Statistics and the basic transformations astronomers expect: moment maps, spectral and spatial statistics, cutouts, spectral slabs, and aligning two maps or cubes onto a common grid. Alignment is always an explicit, recorded derived resource (new data with provenance: method, target grid, interpolation), never a silent change to either input; it uses a validated library (astropy `reproject`), and resolution matching stays a separate, explicit step.
- **Design docs may name SSH and Slurm** in the repository.

## Architecture
1. **Runs.** A Run is a record of one pipeline execution: parameters, input reference, code version (a "-dirty" commit is shown as not reproducible), output files, status. A VGT adapter reads `run_metadata.toml`, links the output products, pairs the reliability map and proposes the axial interpretation, labelled as coming from the pipeline adapter (overridable), not from the file. A run's link to its input cube is a hint by name and size, never identity.
2. **Remote hosts.** A host is a named ssh destination plus remote source roots and a Python command. The plane worker runs on the remote over `ssh host python -P worker.py` with the same line protocol, so only planes travel and cubes stay put. The worker script is deployed by content hash. The remote worker enforces its roots itself (real-path containment); caches key on host and the remote stat signature; concurrency and timeouts are bounded per host.
3. **Jobs.** A job record moves draft, approved, submitted, running, then done, failed or cancelled. The exact script is stored and hashed; submission checks the hash. Status comes from `squeue` and `sacct`; cancel is `scancel`; finished outputs register as a Run. Heavy work goes through Slurm, never the login node. Commands over ssh are fixed templates with validated arguments, never a caller-built shell string.
4. **Workspace links.** Files, runs, jobs, figures and boards reference each other by id, so a figure shows its run and parameters and a run lists the boards that use it. Reviewer views show run parameters and the code version but never host names, user names or paths (the reviewer whitelist test is extended).

## Alternatives
An SSH library inside the API (credentials and host-key handling become ours); a daemon installed on each host (more to deploy and trust); letting the other laptop use the app remotely (needs the authentication design); sshfs mounts (stalls and silent partial reads).

## Consequences
Everything remote depends on the user's ssh setup. The API gains a process boundary per host. Authentication remains a prerequisite for any non-owner action.

## Validation
Each slice ships with tests against a local fake host (an sshd or shim with stand-in `sbatch`, `squeue`, `sacct`), exposure tests for what a reviewer can see, and the browser walk-through extended.

## Rollback / Migration
New tables and routes only; removing them leaves v0.1 data untouched.
