# .zshenv
# Author: Kennt Kim
# Created: 2026-10-19 · Last updated: 2026-10-23
#
# Purpose: loads the terminal engine's shell integration into zsh (Docs/SDTERM-2.md §4.3) without
# touching the user's files: the session starts zsh with ZDOTDIR naming this directory, so zsh reads
# this file first. It puts the user's ZDOTDIR back (kept in SDTERM_ZDOTDIR, or unset), reads the
# user's own .zshenv as zsh would have, and loads sdterm.zsh into an interactive shell. zsh then
# reads the rest of the user's startup files from their own place.
#
# Notes:
# - Taken from SiliconDesk's host/internal/shellinteg/scripts/zsh/.zshenv on
#   2026-10-23. Where these notes say "the session", read whoever starts the shell: the
#   engine's user (Docs/DESIGN.md §5.1).

_sdterm_dir=$ZDOTDIR
if [[ -n ${SDTERM_ZDOTDIR+x} ]]; then
    builtin export ZDOTDIR=$SDTERM_ZDOTDIR
else
    builtin unset ZDOTDIR
fi
builtin unset SDTERM_ZDOTDIR

if [[ -f ${ZDOTDIR:-$HOME}/.zshenv ]]; then
    builtin source ${ZDOTDIR:-$HOME}/.zshenv
fi

if [[ -o interactive && -r $_sdterm_dir/sdterm.zsh ]]; then
    builtin source $_sdterm_dir/sdterm.zsh
fi
builtin unset _sdterm_dir
