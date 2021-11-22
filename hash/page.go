package hash

/*
 * routines dealing with a data page
 *
 * page format:
 *	┌───┬────────┬────────┬────────┐
 * p	│ n │ keyoff │ datoff │ keyoff │
 * 	├───┴────┬───┴───┬────┴──┬─────┤
 *	│ datoff │ free  │  ptr  │ ──> │
 *	├────────┴───────┴───────┴─────┤
 *	│	 F R E E A R E A       │
 *	├──────────────┬───────────────┤
 *	│  <──── ─ ─ ─ │ data	       │
 *	├────────┬─────┴────┬──────────┤
 *	│  key   │ data     │ key      │
 *	└────────┴──────────┴──────────┘
 *
 * Pointer to the free space is always:  p[p[0] + 2]
 * Amount of free space on the page is:  p[p[0] + 1]
 */
