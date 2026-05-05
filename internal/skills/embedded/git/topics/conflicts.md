# Conflicts

**Read when:** a rebase, merge, cherry-pick, or `stash pop` has paused on conflicts and you need to resolve them cleanly.

Conflicts look the same regardless of which operation triggered them — only the "continue" command differs.

## The anatomy

```text
<<<<<<< HEAD
current branch's version
=======
incoming branch's version
>>>>>>> abc1234 (commit subject)
```

- Between `<<<<<<<` and `=======` is "ours" (current branch during merge/rebase).
- Between `=======` and `>>>>>>>` is "theirs" (the commit being applied).
- During a **rebase**, "ours" is confusingly the branch you're rebasing **onto** (because rebase replays your commits on top of that branch). Mnemonic: "ours = the side we're sitting on right now." `git status` will tell you.

## Resolve

1. `git status` — lists paths with `both modified`, `both added`, or `deleted by us/them`.
2. Open each file, decide: keep ours, keep theirs, combine, or write something new. Remove *all* marker lines.
3. `git add <path>` for each resolved file (or `git rm <path>` if the resolution is "delete").
4. Continue the operation:
   - Rebase: `git rebase --continue`
   - Merge: `git merge --continue` (or just `git commit` on older git)
   - Cherry-pick: `git cherry-pick --continue`
   - Stash pop: after `git add`, the stash has already been "popped" — drop it once satisfied with `git stash drop stash@{0}`.
5. To back out instead:
   - `git rebase --abort` / `git merge --abort` / `git cherry-pick --abort`.
   - For a failed `stash pop`, the stash is still in the list. Revert conflicted files with `git restore --source=HEAD <paths>` and leave the stash alone.

## Shortcuts

```bash
# Take our version wholesale
git checkout --ours <path> && git add <path>

# Take theirs wholesale
git checkout --theirs <path> && git add <path>
```

Use sparingly — wholesale picking is the source of "lost work" bugs later. When a conflict is a real semantic clash, resolve by hand.

## Three-way view

```bash
# Shows base + ours + theirs with configured diff3 markers
git config --global merge.conflictstyle diff3    # one-time
# or even better (git ≥ 2.35):
git config --global merge.conflictstyle zdiff3
```

`diff3` adds a `|||||||` section showing the common ancestor, which makes it obvious which side actually changed.

## Rerere — remember resolutions

```bash
git config --global rerere.enabled true
```

Next time the same conflict appears, git replays your resolution automatically. Useful when you rebase the same branch against an evolving main repeatedly.

Clear rerere's cache for a specific conflict: `git rerere forget <path>`.

## Mergetool (optional)

```bash
# Configure once (examples)
git config --global merge.tool vscode
git config --global mergetool.vscode.cmd 'code --wait $MERGED'
git config --global mergetool.keepBackup false

# Invoke during a conflict
git mergetool
```

For headless agents, the inline markers approach is almost always faster than launching a tool.

## Gotchas

- **Binary conflicts** aren't marker-based. `git status` shows them as "both modified"; you can't edit them. Use `git checkout --ours/--theirs <path>` or regenerate them, then `git add`.
- **`both deleted`:** either side removed the file. If the resolution is "delete", `git rm <path>`. If keep, `git checkout <ref> -- <path>` to restore.
- **Empty diff after resolution:** during rebase, if your resolution produces the same tree as the parent, git may offer to drop the commit. Usually `--skip` is correct — but check that you intended that commit to contribute *something*.
- **Premature `git add`:** once you stage a file with conflict markers still in it, git treats them as literal text. Always verify with `git diff --cached` before continuing.
