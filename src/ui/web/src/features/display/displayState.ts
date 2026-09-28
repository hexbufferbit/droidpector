// Zoom state of the Android display (shared by the phone canvas and its toolbar), persisted.
import { loadJSON, saveJSON } from '../../lib/storage';
import { createStore } from '../../state/store';
import { clampZoom, sanitizeZoom, zoomStep, type ZoomMode } from './protocol';

export const ZOOM_STORAGE_KEY = 'droidpector.display.zoom';

export interface DisplayViewState {
  zoom: ZoomMode;
  /** CSS pixels per framebuffer pixel currently shown (fit mode resolves to a number) */
  effectiveScale: number;
}

export const displayStore = createStore<DisplayViewState>({
  zoom: sanitizeZoom(loadJSON<unknown>(ZOOM_STORAGE_KEY, null)),
  effectiveScale: 0,
});

let lastSaved = displayStore.get().zoom;
displayStore.subscribe(() => {
  const z = displayStore.get().zoom;
  if (z !== lastSaved) {
    lastSaved = z;
    saveJSON(ZOOM_STORAGE_KEY, z);
  }
});

export const zoomActions = {
  fit: () => displayStore.set({ zoom: { mode: 'fit' } }),
  oneToOne: () => displayStore.set({ zoom: { mode: 'scale', scale: 1 } }),
  set: (scale: number) => displayStore.set({ zoom: { mode: 'scale', scale: clampZoom(scale) } }),
  /** step zooms in (+1) or out (−1) from the currently shown scale. */
  step(dir: 1 | -1) {
    const { zoom, effectiveScale } = displayStore.get();
    const cur = zoom.mode === 'scale' ? zoom.scale : effectiveScale || 1;
    displayStore.set({ zoom: { mode: 'scale', scale: zoomStep(cur, dir) } });
  },
};
