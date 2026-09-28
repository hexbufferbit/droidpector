// dist/ is go:embed-ed into the executable and tracked through dist/.keep,
// which vite's emptyOutDir removes: recreate it after every build.
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

writeFileSync(fileURLToPath(new URL('../dist/.keep', import.meta.url)), '');
