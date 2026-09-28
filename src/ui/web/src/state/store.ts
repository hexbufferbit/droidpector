// A tiny external store usable with React's useSyncExternalStore.
import { useSyncExternalStore } from 'react';

export interface Store<T> {
  get(): T;
  set(update: Partial<T> | ((s: T) => Partial<T>)): void;
  subscribe(fn: () => void): () => void;
}

export function createStore<T extends object>(initial: T): Store<T> {
  let state = initial;
  const subs = new Set<() => void>();
  return {
    get: () => state,
    set(update) {
      const patch = typeof update === 'function' ? update(state) : update;
      let changed = false;
      for (const k of Object.keys(patch) as (keyof T)[]) {
        if (!Object.is(state[k], patch[k])) {
          changed = true;
          break;
        }
      }
      if (!changed) return;
      state = { ...state, ...patch };
      for (const fn of [...subs]) fn();
    },
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
  };
}

/** useStore subscribes to a slice of a store. The selector must return a stable value (a field, a primitive). */
export function useStore<T extends object, S>(store: Store<T>, selector: (s: T) => S): S {
  return useSyncExternalStore(
    store.subscribe,
    () => selector(store.get()),
    () => selector(store.get()),
  );
}

/** A minimal event emitter. */
export class Emitter<A extends unknown[]> {
  private subs = new Set<(...args: A) => void>();
  on(fn: (...args: A) => void): () => void {
    this.subs.add(fn);
    return () => this.subs.delete(fn);
  }
  emit(...args: A): void {
    for (const fn of [...this.subs]) fn(...args);
  }
}
