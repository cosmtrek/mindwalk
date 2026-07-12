import { create } from "zustand";
import type { CityMap, SessionMeta, Trace } from "../types";
import { loadFilters, saveFilters } from "./filters";

export type SceneView = "tree" | "terrain";

interface AppState {
  sessions: SessionMeta[];
  activeSessionKey?: string;
  trace?: Trace;
  city?: CityMap;
  currentSeq: number;
  selectedPath?: string;
  view: SceneView;
  loading: boolean;
  error?: string;
  hideEmpty: boolean;
  harnessFilter?: string;
  railCollapsed: boolean;
  mapOnly: boolean;
  setView: (view: SceneView) => void;
  setSessions: (sessions: SessionMeta[]) => void;
  setActiveSession: (key?: string) => void;
  setData: (trace: Trace, city: CityMap) => void;
  setCityOnly: (city: CityMap) => void;
  setCurrentSeq: (seq: number) => void;
  setSelectedPath: (path?: string) => void;
  setLoading: (loading: boolean) => void;
  setError: (error?: string) => void;
  setHideEmpty: (hideEmpty: boolean) => void;
  setHarnessFilter: (harness?: string) => void;
  setRailCollapsed: (collapsed: boolean) => void;
}

const initialFilters = loadFilters();

const RAIL_COLLAPSED_KEY = "mindwalk.railCollapsed";

function loadRailCollapsed(): boolean {
  try {
    return localStorage.getItem(RAIL_COLLAPSED_KEY) === "1";
  } catch {
    return false;
  }
}

export const useAppStore = create<AppState>((set, get) => ({
  sessions: [],
  currentSeq: 0,
  view: "tree",
  loading: false,
  hideEmpty: initialFilters.hideEmpty,
  harnessFilter: initialFilters.harness,
  railCollapsed: loadRailCollapsed(),
  mapOnly: false,
  setView: (view) => set({ view }),
  setSessions: (sessions) => set({ sessions }),
  setActiveSession: (activeSessionKey) =>
    set({ activeSessionKey, trace: undefined, city: undefined, currentSeq: 0, selectedPath: undefined }),
  setData: (trace, city) => set({ trace, city, currentSeq: Math.max(0, trace.events.length - 1) }),
  // static full-repo map: render the city with no session/trace attached
  setCityOnly: (city) => set({ city, trace: undefined, currentSeq: 0, selectedPath: undefined, mapOnly: true }),
  setCurrentSeq: (currentSeq) => set({ currentSeq }),
  setSelectedPath: (selectedPath) => set({ selectedPath }),
  setLoading: (loading) => set({ loading }),
  setError: (error) => set({ error }),
  setHideEmpty: (hideEmpty) => {
    set({ hideEmpty });
    saveFilters({ hideEmpty, harness: get().harnessFilter });
  },
  setHarnessFilter: (harnessFilter) => {
    set({ harnessFilter });
    saveFilters({ hideEmpty: get().hideEmpty, harness: harnessFilter });
  },
  setRailCollapsed: (railCollapsed) => {
    set({ railCollapsed });
    try {
      localStorage.setItem(RAIL_COLLAPSED_KEY, railCollapsed ? "1" : "0");
    } catch {
      // storage unavailable: preference resets on next load
    }
  }
}));
