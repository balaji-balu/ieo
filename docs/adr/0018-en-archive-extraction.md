# 0018. EN archive extraction: staging, containment and links

Status: Proposed · Date: 2026-10-10 · Spec: §5.2, §8.9, §9.1, §9.2 · Refines: 0009

## Context
Roadmap slice D2a (balaji-balu/ieo#58) builds `internal/en/archive`, which validates a Margo Compose
Archive and extracts it (SPEC §5.2, §9.2). The spec gives the rules and the result; it leaves open:
- how extraction stays inside the component's directory, beyond "check the resolved path";
- what a failed or interrupted extraction leaves on disk;
- when links are created, given "MUST NOT follow links while writing";
- what a host that can't create links, or has no setuid bits, does. The maintainer runs the tests
  on Windows 11 (ADR 0009); real hosts are Linux.

## Decision
- **Staging.** An archive is extracted to `<component>.extracting-<random>` beside the component's
  directory, and that directory is renamed to `<component>/` once the whole archive is valid.
  - The component's directory is deleted before extraction starts. After a failure it does not
    exist: an earlier extraction is not kept.
  - A staging directory is deleted on any failure. One stays only if the EN dies while extracting;
    the EN removes directories with `.extracting-` in their name under `deployments/` when it
    starts (roadmap D5).
- **Containment.** Every write goes through an `os.Root` opened on the staging directory, so the
  operating system refuses a path that leaves it. This is in addition to the checks on each entry's
  path (SPEC §5.2), not in place of them.
- **Links.**
  - Symbolic links are recorded while the archive is read and created after every other entry. No
    link is on disk while files are written, so nothing can be written through one.
  - Before any is created, each is followed to its end through the archive's other symbolic links,
    for at most 40 links. One that leaves the top-level directory on the way is invalid, even if
    its target reads as inside (`a -> .`, `l -> a/../x`).
  - Hard links are created as hard links, when their entry is read.
- **Modes.** A file or directory gets the entry's nine permission bits and no others; the process
  umask is not applied. A directory also gets owner read, write and search, so the EN can fill it
  and later delete it. A directory no entry names gets 0755. Owners and times are not restored. The component's directory itself has
  mode 0700.
- **Read errors.** An archive that can't be decompressed or parsed is invalid. A failure of the
  reader the archive comes from, or the context ending, is returned as it is and is not
  `IEO-ARCHIVE-INVALID`.
- **PAX global headers** (written by `git archive`) are skipped; they count as entries.
- **Windows.**
  - Entry names with `\`, a drive letter or a reserved name are invalid.
  - Creating a symbolic link needs Developer Mode or a privilege. Without it, an archive with a
    valid symbolic link fails with the system's error. Rejections don't depend on it, because
    links are checked before any is created.
  - Files have no setuid, setgid or sticky bits. The §17.6 test for them checks modes on Linux
    (CI) and only extracts on Windows.

## Consequences
- An invalid archive never appears at the path Compose is given, not even partly.
- Extraction needs the staging directory and the component's directory on one file system; both
  are under `en.data_dir`.
- A component named like another component's staging directory can't exist: component names come
  from the deployment, and the random suffix is chosen so the name is unused.
- The check that follows links compares the archive's own names, byte for byte. On a file system
  that ignores case (Windows, macOS), a link reached under another spelling (`A -> .`, then
  `l -> a/../x`) is not followed by the check, so such a link can point outside the top-level
  directory. Nothing is written through it, since links are created last. Hosts are Linux, where
  names are compared exactly; an EN on a file system that ignores case needs this closed first.
- An entry name the file system refuses (a segment over 255 bytes) fails with the system's error,
  not as an invalid archive.
- A file stored in the old GNU sparse format is not accepted as a regular file. `tar` writes it
  only with `--sparse`; the PAX formats are read as regular files.
- A workload that needs a file owned by another user, or with special bits, sets that in its
  container image, not in the archive.

## Options considered
- Join each path to the directory and check it with `filepath.EvalSymlinks` before writing: the
  check and the write are separate steps, and a link made between them is followed.
- Create symbolic links as they are read and refuse entries below them: links are then on disk
  while files are written, so safety depends on every check being right, and a host without the
  privilege fails before it can reject a bad archive.
- Read the archive twice, validating first: the second pass must repeat every check anyway, since
  it is the one that writes.
- Copy the target instead of making a hard link: the bytes would count twice against
  `en.archive.max_extracted_bytes`, and the two files would no longer be one.
