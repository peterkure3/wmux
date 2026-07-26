Refactor the wmux project using solid pinciples, reorganize the file structure.
on opening the program, the user should go straight to the tui.

Some of the commands have too many flags, its tiring to type the flags everytime.

The flags can be optional and if i need more fine grained control over what i want wmux todo i'll use the full command syntax.

Panes and the sidebar should support mouse clicks as the focus shifts to the sidebar or pane(s) respectively
there should be a keyboard shortcut to switch to modes such that the other functions like splitting the pane horizontally or vertically can be done.

if opening a grid layout with wmux, this format would be good wmux grid 4, then the panes are arranged in a 4x4 grid.

if i want to open a grid layout with claude opening in those grids the command would be wmux grid 4 --claude|codex|kimi|mimo

the grid layout can also be 2,3,4 panes if needed by the user.
the command to cycle through panes would be ctrl + o



