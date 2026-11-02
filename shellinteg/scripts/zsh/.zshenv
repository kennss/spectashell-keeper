# .zshenv
# Author: Kennt Kim
# Created: 2026-10-19 · Last updated: 2026-11-02
#
# Purpose: loads the terminal engine's shell integration into zsh (SiliconDesk's Docs/SDTERM-2.md
# §4.3) without touching the user's files: the session starts zsh with ZDOTDIR naming this
# directory, so zsh reads this file first. It puts the user's ZDOTDIR back (kept in
# SPECTASHELL_ZDOTDIR, or unset), reads the user's own .zshenv as zsh would have, and loads
# spectashell.zsh into an interactive shell. zsh then reads the rest of the user's startup files
# from their own place.
#
# Notes:
# - Taken from SiliconDesk's host/internal/shellinteg/scripts/zsh/.zshenv on
#   2026-10-23. Where these notes say "the session", read whoever starts the shell: the
#   engine's user (Docs/design/02-CONNECTIONS.md §1).

_spectashell_dir=$ZDOTDIR
if [[ -n ${SPECTASHELL_ZDOTDIR+x} ]]; then
    builtin export ZDOTDIR=$SPECTASHELL_ZDOTDIR
else
    builtin unset ZDOTDIR
fi
builtin unset SPECTASHELL_ZDOTDIR

if [[ -f ${ZDOTDIR:-$HOME}/.zshenv ]]; then
    builtin source ${ZDOTDIR:-$HOME}/.zshenv
fi

if [[ -o interactive && -r $_spectashell_dir/spectashell.zsh ]]; then
    builtin source $_spectashell_dir/spectashell.zsh
fi
builtin unset _spectashell_dir
