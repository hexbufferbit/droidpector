// APK file selection helpers (file picker + drag & drop).

export function isApkFile(f: { name: string }): boolean {
  return /\.apk$/i.test(f.name);
}

/** firstApk returns the first .apk of a file list, or the first file (the core explains why others fail). */
export function firstApk(files: FileList | File[] | null | undefined): File | null {
  if (!files || files.length === 0) return null;
  const list = Array.from(files);
  return list.find(isApkFile) ?? list[0];
}

/** dragHasFiles tells whether a drag carries files. */
export function dragHasFiles(dt: DataTransfer | null): boolean {
  return !!dt && Array.from(dt.types ?? []).includes('Files');
}

let input: HTMLInputElement | null = null;

/** pickApk opens the system file picker for an .apk file. */
export function pickApk(onFile: (f: File) => void): void {
  input?.remove();
  input = document.createElement('input');
  input.type = 'file';
  input.accept = '.apk,application/vnd.android.package-archive';
  input.style.display = 'none';
  input.onchange = () => {
    const f = firstApk(input?.files);
    if (f) onFile(f);
    input?.remove();
    input = null;
  };
  document.body.appendChild(input);
  input.click();
}
