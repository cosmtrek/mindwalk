import { useEffect, useMemo, useRef } from "react";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import { touchWord, type CityFile, type CityMap, type Touch } from "../types";
import type { FilePlayback } from "../playback/reducer";
import { DirLabelSet } from "./dirLabels";
import {
  disposeGroup,
  ensureVisible,
  fitDistance,
  prefersReducedMotion,
  SceneTip,
  SKY,
  touchColors
} from "./sceneUtils";
import { computeTreeLayout, type TreeLayout } from "./treeLayout";
import { fireflyTexture, haloTexture } from "./textures";
import { TrailRenderer } from "./trail";

interface TreeSceneProps {
  city?: CityMap;
  playback: FilePlayback;
  selectedPath?: string;
  onSelect: (path?: string) => void;
  onCanvasReady?: (canvas: HTMLCanvasElement | null) => void;
}

// Firefly tree: the repo is a radial tree — directories fork, files are
// leaves. The walker is a firefly that lights leaves as it lands; attention
// depth shows as the pool of light it leaves behind on the ground.
//
// One variable, one channel: leaf color says WHAT happened (touch state),
// halo radius says HOW MUCH (visits), branch brightness says WHERE the
// light is, and the selection is a shape (ring + beam), never a recolor.
const colors: Record<Touch | "unvisited" | "ghost" | "selected", THREE.Color> = {
  unvisited: new THREE.Color("#5a6375"),
  ghost: new THREE.Color("#4d5464"),
  ...touchColors
};
const EDGE_BASE = new THREE.Color("#3c424f");
// branches leading to visited leaves brighten, but stay neutral: the branch
// guides the eye, the leaf carries the classification
const EDGE_LIT = new THREE.Color("#7d8496");
// white-hot walker: keeps the firefly apart from edit-amber leaves
const FIREFLY_HOT = new THREE.Color("#ffeeda");
const LEAF_Y = 0.7;

// halo radius encodes revisits only — touch type already lives in the leaf
// color, and doubling it up here made the radius unreadable
function haloRadius(visits: number): number {
  return Math.min(1.8 * (1 + 0.45 * Math.log2(Math.max(visits, 1))), 4.8);
}

interface HaloSlot {
  fileId: number;
  target: number;
  color: THREE.Color;
}

const LABEL_Y = 1.8;
// the inspector docks on the right; selection pans the camera clear of it
const INSPECTOR_RESERVED_PX = 348;

export function TreeScene({ city, playback, selectedPath, onSelect, onCanvasReady }: TreeSceneProps) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const leafMeshRef = useRef<THREE.InstancedMesh | null>(null);
  const ghostMeshRef = useRef<THREE.InstancedMesh | null>(null);
  const ghostIndexRef = useRef<Map<number, number>>(new Map());
  const selectionRef = useRef<{ ring: THREE.Mesh; beam: THREE.Mesh } | null>(null);
  const haloMeshRef = useRef<THREE.InstancedMesh | null>(null);
  const edgesRef = useRef<THREE.LineSegments | null>(null);
  const edgeMetaRef = useRef<{ childPath?: string; childFileId?: number; vertexCount: number }[]>([]);
  const filesRef = useRef<CityFile[]>([]);
  const layoutRef = useRef<TreeLayout | null>(null);
  const slotsRef = useRef<HaloSlot[]>([]);
  const radiiRef = useRef<Map<number, number>>(new Map());
  const rendererRef = useRef<THREE.WebGLRenderer | null>(null);
  const cameraRef = useRef<THREE.PerspectiveCamera | null>(null);
  const controlsRef = useRef<OrbitControls | null>(null);
  const sceneRef = useRef<THREE.Scene | null>(null);
  const groupRef = useRef<THREE.Group | null>(null);
  const trailRef = useRef<TrailRenderer | null>(null);
  const fireflyRef = useRef<THREE.Sprite | null>(null);
  const labelSetRef = useRef<DirLabelSet | null>(null);
  const frameRef = useRef<number | null>(null);
  const reducedRef = useRef(false);
  // handlers live in the mount effect; they read playback through this ref
  const playbackRef = useRef(playback);
  playbackRef.current = playback;
  // camera fit deferred while the viewport reports no size (hidden pane,
  // background tab); resize retries it instead of leaving the camera at NaN
  const fitPendingRef = useRef<(() => boolean) | null>(null);

  const layout = useMemo(() => (city && city.files.length > 0 ? computeTreeLayout(city.files) : null), [city]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const reduced = prefersReducedMotion();
    reducedRef.current = reduced;

    const scene = new THREE.Scene();
    scene.background = SKY;
    sceneRef.current = scene;

    const camera = new THREE.PerspectiveCamera(38, host.clientWidth / host.clientHeight || 1, 0.1, 2400);
    camera.position.set(60, 110, 90);
    cameraRef.current = camera;

    const renderer = new THREE.WebGLRenderer({ antialias: true });
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    renderer.setSize(host.clientWidth, host.clientHeight);
    rendererRef.current = renderer;
    host.appendChild(renderer.domElement);
    onCanvasReady?.(renderer.domElement);

    const controls = new OrbitControls(camera, renderer.domElement);
    controls.enableDamping = !reduced;
    controls.dampingFactor = 0.08;
    controls.maxPolarAngle = Math.PI * 0.44;
    controls.autoRotate = !reduced;
    controls.autoRotateSpeed = -0.5;
    const tip = new SceneTip(host);
    controls.addEventListener("start", () => {
      controls.autoRotate = false;
      tip.hide();
    });
    controlsRef.current = controls;

    const sky = new THREE.HemisphereLight("#66779b", "#161922", 1.7);
    scene.add(sky);
    const moon = new THREE.DirectionalLight("#b6c5de", 1.1);
    moon.position.set(-60, 120, -40);
    scene.add(moon);

    // screen-space nearest-leaf picking: tiny orbs are hostile raycast
    // targets, so pick whatever leaf lands closest to the pointer
    const PICK_RADIUS = 18;
    const projected = new THREE.Vector3();
    const pickFile = (event: PointerEvent): CityFile | undefined => {
      const treeLayout = layoutRef.current;
      if (!treeLayout || !cameraRef.current || !rendererRef.current) return undefined;
      const rect = rendererRef.current.domElement.getBoundingClientRect();
      const px = event.clientX - rect.left;
      const py = event.clientY - rect.top;
      let best: CityFile | undefined;
      let bestD2 = PICK_RADIUS * PICK_RADIUS;
      for (const file of filesRef.current) {
        const pos = treeLayout.leaf.get(file.id);
        if (!pos) continue;
        projected.set(pos.x, LEAF_Y, pos.z).project(cameraRef.current);
        if (projected.z > 1) continue;
        const sx = ((projected.x + 1) / 2) * rect.width;
        const sy = ((1 - projected.y) / 2) * rect.height;
        const d2 = (sx - px) * (sx - px) + (sy - py) * (sy - py);
        if (d2 < bestD2) {
          bestD2 = d2;
          best = file;
        }
      }
      return best;
    };

    let downAt: { x: number; y: number } | null = null;
    const onPointerDown = (event: PointerEvent) => {
      downAt = { x: event.clientX, y: event.clientY };
    };
    const onPointerUp = (event: PointerEvent) => {
      if (!downAt) return;
      const moved = Math.hypot(event.clientX - downAt.x, event.clientY - downAt.y);
      downAt = null;
      if (moved > 5) return;
      onSelect(pickFile(event)?.path);
    };
    // hover: cursor + one-line readout, throttled to one pick per frame
    let hoverRaf = 0;
    const onPointerMove = (event: PointerEvent) => {
      if (downAt) {
        tip.hide();
        return;
      }
      if (hoverRaf) return;
      hoverRaf = requestAnimationFrame(() => {
        hoverRaf = 0;
        const file = pickFile(event);
        renderer.domElement.style.cursor = file ? "pointer" : "";
        if (!file) {
          tip.hide();
          return;
        }
        const snapshot = playbackRef.current;
        const touch = snapshot.touchByPath.get(file.path);
        const visits = snapshot.visitsByFile.get(file.id) ?? 0;
        const meta = touch
          ? `${touchWord(touch)} · ${visits} visit${visits === 1 ? "" : "s"}`
          : touchWord(undefined);
        tip.show(file.path, file.ghost ? `${meta} · ghost` : meta, event.clientX, event.clientY);
      });
    };
    const onPointerLeave = () => {
      tip.hide();
      renderer.domElement.style.cursor = "";
    };
    renderer.domElement.addEventListener("pointerdown", onPointerDown);
    renderer.domElement.addEventListener("pointerup", onPointerUp);
    renderer.domElement.addEventListener("pointermove", onPointerMove);
    renderer.domElement.addEventListener("pointerleave", onPointerLeave);

    const resize = () => {
      if (!hostRef.current) return;
      const w = hostRef.current.clientWidth;
      const h = hostRef.current.clientHeight;
      if (w === 0 || h === 0) return;
      renderer.setSize(w, h);
      camera.aspect = w / h;
      camera.updateProjectionMatrix();
      if (fitPendingRef.current?.()) fitPendingRef.current = null;
    };
    const observer = new ResizeObserver(resize);
    observer.observe(host);

    const clock = new THREE.Clock();
    const matrix = new THREE.Matrix4();
    const quaternion = new THREE.Quaternion().setFromEuler(new THREE.Euler(-Math.PI / 2, 0, 0));
    const render = () => {
      controls.update();
      labelSetRef.current?.updateTargets(camera, renderer.domElement.clientWidth, renderer.domElement.clientHeight);
      labelSetRef.current?.ease(reducedRef.current);

      // pools of light grow toward their attention radius
      const halos = haloMeshRef.current;
      const slots = slotsRef.current;
      const radii = radiiRef.current;
      const treeLayout = layoutRef.current;
      if (halos && treeLayout && slots.length > 0) {
        let moving = false;
        for (let i = 0; i < slots.length; i++) {
          const slot = slots[i];
          const pos = treeLayout.leaf.get(slot.fileId);
          if (!pos) continue;
          let cur = radii.get(slot.fileId) ?? 0;
          const diff = slot.target - cur;
          if (Math.abs(diff) > 0.015) {
            cur = reducedRef.current ? slot.target : cur + diff * 0.12;
            radii.set(slot.fileId, cur);
            moving = true;
          } else if (cur !== slot.target) {
            radii.set(slot.fileId, slot.target);
            cur = slot.target;
            moving = true;
          }
          matrix.compose(
            new THREE.Vector3(pos.x, 0.06, pos.z),
            quaternion,
            new THREE.Vector3(Math.max(cur, 0.01) * 2, Math.max(cur, 0.01) * 2, 1)
          );
          halos.setMatrixAt(i, matrix);
        }
        if (moving) halos.instanceMatrix.needsUpdate = true;
      }

      const firefly = fireflyRef.current;
      if (firefly?.visible) {
        const t = clock.getElapsedTime();
        const base = firefly.userData.baseScale as number;
        const pulse = reducedRef.current ? 1 : 1 + 0.1 * Math.sin(t * 2.4);
        firefly.scale.setScalar(base * pulse);
      }
      renderer.render(scene, camera);
      frameRef.current = requestAnimationFrame(render);
    };
    render();

    return () => {
      if (frameRef.current) cancelAnimationFrame(frameRef.current);
      if (hoverRaf) cancelAnimationFrame(hoverRaf);
      renderer.domElement.removeEventListener("pointerdown", onPointerDown);
      renderer.domElement.removeEventListener("pointerup", onPointerUp);
      renderer.domElement.removeEventListener("pointermove", onPointerMove);
      renderer.domElement.removeEventListener("pointerleave", onPointerLeave);
      tip.dispose();
      observer.disconnect();
      controls.dispose();
      renderer.dispose();
      host.removeChild(renderer.domElement);
      scene.clear();
      onCanvasReady?.(null);
    };
  }, [onSelect, onCanvasReady]);

  // grow the skeleton: ground, edges, leaves, labels
  useEffect(() => {
    const scene = sceneRef.current;
    if (!scene) return;
    filesRef.current = city?.files ?? [];
    layoutRef.current = layout;
    slotsRef.current = [];
    radiiRef.current = new Map();
    if (!city || !layout) return;

    const group = new THREE.Group();
    const size = layout.radius * 2.3;

    scene.fog = new THREE.Fog(SKY, size * 2.1, size * 4.2);

    const ground = new THREE.Mesh(
      new THREE.PlaneGeometry(size * 6, size * 6),
      new THREE.MeshStandardMaterial({ color: "#14171e", roughness: 1 })
    );
    ground.rotation.x = -Math.PI / 2;
    ground.position.y = -0.25;
    group.add(ground);

    const grid = new THREE.GridHelper(size * 2.4, 40, "#1d222c", "#181c25");
    (grid.material as THREE.Material).transparent = true;
    (grid.material as THREE.Material).opacity = 0.4;
    grid.position.y = -0.24;
    group.add(grid);

    // branch skeleton, one segment soup with per-vertex colors
    const positions: number[] = [];
    const edgeMeta: { childPath?: string; childFileId?: number; vertexCount: number }[] = [];
    for (const edge of layout.edges) {
      let count = 0;
      for (let i = 0; i < edge.points.length - 1; i++) {
        positions.push(edge.points[i].x, 0.12, edge.points[i].z);
        positions.push(edge.points[i + 1].x, 0.12, edge.points[i + 1].z);
        count += 2;
      }
      edgeMeta.push({ childPath: edge.childPath, childFileId: edge.childFileId, vertexCount: count });
    }
    const edgeGeo = new THREE.BufferGeometry();
    edgeGeo.setAttribute("position", new THREE.Float32BufferAttribute(positions, 3));
    const edgeColors = new Float32Array(positions.length);
    edgeGeo.setAttribute("color", new THREE.BufferAttribute(edgeColors, 3));
    const edges = new THREE.LineSegments(
      edgeGeo,
      new THREE.LineBasicMaterial({ vertexColors: true, transparent: true, opacity: 0.9 })
    );
    edgesRef.current = edges;
    edgeMetaRef.current = edgeMeta;
    group.add(edges);

    // leaves: one orb per file, sized by sqrt(lines) — solid for files on
    // disk, wireframe for ghosts (touched in the session, gone from the
    // repo): shape separates them where a third grey never could.
    // state colors must not fade with distance, so leaf-like materials
    // opt out of the fog; only the ground plane keeps the depth cue
    const leafGeo = new THREE.SphereGeometry(0.5, 10, 8);
    const leafMat = new THREE.MeshBasicMaterial({ toneMapped: false, fog: false });
    const leaves = new THREE.InstancedMesh(leafGeo, leafMat, city.files.length);
    leaves.instanceColor = new THREE.InstancedBufferAttribute(new Float32Array(city.files.length * 3), 3);
    const ghostFiles = city.files.filter((file) => file.ghost);
    const ghostIndex = new Map<number, number>();
    ghostFiles.forEach((file, i) => ghostIndex.set(file.id, i));
    ghostIndexRef.current = ghostIndex;
    const matrix = new THREE.Matrix4();
    const hidden = new THREE.Matrix4().makeScale(0, 0, 0);
    for (const file of city.files) {
      const pos = layout.leaf.get(file.id);
      if (!pos) continue;
      if (file.ghost) {
        leaves.setMatrixAt(file.id, hidden);
        continue;
      }
      const scale = Math.min(0.24 + Math.sqrt(Math.max(file.lines, 1)) * 0.045, 1.05);
      matrix.compose(new THREE.Vector3(pos.x, LEAF_Y, pos.z), new THREE.Quaternion(), new THREE.Vector3(scale, scale, scale));
      leaves.setMatrixAt(file.id, matrix);
      leaves.setColorAt(file.id, colors.unvisited);
    }
    leaves.instanceMatrix.needsUpdate = true;
    if (leaves.instanceColor) leaves.instanceColor.needsUpdate = true;
    leafMeshRef.current = leaves;
    group.add(leaves);

    let ghosts: THREE.InstancedMesh | null = null;
    if (ghostFiles.length > 0) {
      ghosts = new THREE.InstancedMesh(
        new THREE.SphereGeometry(0.55, 8, 5),
        new THREE.MeshBasicMaterial({ wireframe: true, transparent: true, opacity: 0.85, toneMapped: false, fog: false }),
        ghostFiles.length
      );
      ghosts.instanceColor = new THREE.InstancedBufferAttribute(new Float32Array(ghostFiles.length * 3), 3);
      for (const file of ghostFiles) {
        const pos = layout.leaf.get(file.id);
        const i = ghostIndex.get(file.id)!;
        if (!pos) {
          ghosts.setMatrixAt(i, hidden);
          continue;
        }
        const scale = Math.min(0.24 + Math.sqrt(Math.max(file.lines, 1)) * 0.045, 1.05) * 0.8;
        matrix.compose(new THREE.Vector3(pos.x, LEAF_Y, pos.z), new THREE.Quaternion(), new THREE.Vector3(scale, scale, scale));
        ghosts.setMatrixAt(i, matrix);
        ghosts.setColorAt(i, colors.ghost);
      }
      ghosts.instanceMatrix.needsUpdate = true;
      if (ghosts.instanceColor) ghosts.instanceColor.needsUpdate = true;
      ghosts.raycast = () => undefined;
      group.add(ghosts);
    }
    ghostMeshRef.current = ghosts;

    // pools of light under visited leaves — normal blending with a fixed
    // alpha: additive stacking burned dense directories into one white
    // blob and erased the per-file radius
    const halos = new THREE.InstancedMesh(
      new THREE.PlaneGeometry(1, 1),
      new THREE.MeshBasicMaterial({
        map: haloTexture(),
        transparent: true,
        opacity: 0.55,
        depthWrite: false,
        toneMapped: false,
        fog: false
      }),
      city.files.length
    );
    halos.instanceColor = new THREE.InstancedBufferAttribute(new Float32Array(city.files.length * 3), 3);
    halos.count = 0;
    halos.frustumCulled = false;
    halos.raycast = () => undefined;
    haloMeshRef.current = halos;
    group.add(halos);

    // directory labels: screen-space LOD in the render loop decides which
    // of the biggest subtrees show their name
    labelSetRef.current = new DirLabelSet(layout.dirs, group, LABEL_Y);

    const firefly = new THREE.Sprite(
      new THREE.SpriteMaterial({
        map: fireflyTexture(),
        color: FIREFLY_HOT,
        blending: THREE.AdditiveBlending,
        depthWrite: false,
        transparent: true,
        fog: false
      })
    );
    firefly.userData.baseScale = Math.max(size * 0.026, 2.2);
    firefly.visible = false;
    fireflyRef.current = firefly;
    group.add(firefly);

    // selection is a shape, not a recolor: the ring + beam mark the leaf
    // while its touch color stays intact
    const selectionMat = new THREE.MeshBasicMaterial({
      color: colors.selected,
      transparent: true,
      opacity: 0.9,
      side: THREE.DoubleSide,
      depthWrite: false,
      toneMapped: false,
      fog: false
    });
    const ring = new THREE.Mesh(new THREE.RingGeometry(1.2, 1.5, 48), selectionMat);
    ring.rotation.x = -Math.PI / 2;
    ring.visible = false;
    ring.raycast = () => undefined;
    group.add(ring);
    const beam = new THREE.Mesh(
      new THREE.CylinderGeometry(0.05, 0.05, 6, 6, 1, true),
      new THREE.MeshBasicMaterial({
        color: colors.selected,
        transparent: true,
        opacity: 0.32,
        blending: THREE.AdditiveBlending,
        depthWrite: false,
        toneMapped: false,
        fog: false
      })
    );
    beam.visible = false;
    beam.raycast = () => undefined;
    group.add(beam);
    selectionRef.current = { ring, beam };

    const trail = new TrailRenderer(1.6);
    trailRef.current = trail;
    group.add(trail.object);

    groupRef.current = group;
    scene.add(group);

    // keep the canonical viewing direction, but pull back exactly far
    // enough that every leaf fits the viewport's frustum — nominal radius
    // ignores the aspect ratio and overflows short windows
    const fitView = (): boolean => {
      const camera = cameraRef.current;
      const controls = controlsRef.current;
      if (!camera || !controls) return true;
      const dir = new THREE.Vector3(0.3, 0.66, 0.47).normalize();
      const points = [...layout.leaf.values()].map((pos) => new THREE.Vector3(pos.x, LEAF_Y, pos.z));
      const fitted = fitDistance(camera, dir, points);
      if (fitted === null) return false;
      // breathing room so edge leaves clear the HUD overlays
      const distance = fitted * 1.12;
      camera.position.copy(dir).multiplyScalar(distance);
      controls.target.set(0, 0, 0);
      controls.minDistance = size * 0.15;
      controls.maxDistance = Math.max(size * 2.4, distance * 1.2);
      controls.update();
      return true;
    };
    fitPendingRef.current = fitView() ? null : fitView;

    return () => {
      fitPendingRef.current = null;
      disposeGroup(group);
      scene.remove(group);
      groupRef.current = null;
      leafMeshRef.current = null;
      ghostMeshRef.current = null;
      ghostIndexRef.current = new Map();
      selectionRef.current = null;
      haloMeshRef.current = null;
      edgesRef.current = null;
      trailRef.current = null;
      fireflyRef.current = null;
      labelSetRef.current = null;
    };
  }, [city, layout]);

  // playback → leaf colors, halo targets, branch tinting
  useEffect(() => {
    const leaves = leafMeshRef.current;
    const ghosts = ghostMeshRef.current;
    const ghostIndex = ghostIndexRef.current;
    const halos = haloMeshRef.current;
    const edges = edgesRef.current;
    if (!leaves || !halos || !edges || !city || !layout) return;

    const radii = radiiRef.current;
    const slots: HaloSlot[] = [];
    const present = new Set<number>();
    // directories on a path to a touched file — branches brighten as a
    // single neutral tone, classification stays on the leaves
    const litDirs = new Set<string>();
    for (const file of city.files) {
      const touch = playback.touchByFile.get(file.id);
      let leafColor = file.ghost ? colors.ghost : colors.unvisited;
      if (touch) {
        leafColor = colors[touch];
        const visits = playback.visitsByFile.get(file.id) ?? 1;
        slots.push({ fileId: file.id, target: haloRadius(visits), color: colors[touch] });
        present.add(file.id);
        const parts = file.path.split("/");
        let path = "";
        for (let i = 0; i < parts.length - 1; i++) {
          path = path ? `${path}/${parts[i]}` : parts[i];
          litDirs.add(path);
        }
      }
      if (file.ghost) {
        const i = ghostIndex.get(file.id);
        if (ghosts && i !== undefined) ghosts.setColorAt(i, leafColor);
      } else {
        leaves.setColorAt(file.id, leafColor);
      }
    }
    for (const [fileId, cur] of radii) {
      if (cur > 0.04 && !present.has(fileId)) {
        slots.push({ fileId, target: 0, color: colors.unvisited });
      } else if (cur <= 0.04 && !present.has(fileId)) {
        radii.delete(fileId);
      }
    }
    slots.forEach((slot, i) => halos.setColorAt(i, slot.color));
    halos.count = slots.length;
    if (halos.instanceColor) halos.instanceColor.needsUpdate = true;
    if (leaves.instanceColor) leaves.instanceColor.needsUpdate = true;
    if (ghosts?.instanceColor) ghosts.instanceColor.needsUpdate = true;
    slotsRef.current = slots;

    // brighten branches that lead to light
    const colorAttr = edges.geometry.getAttribute("color") as THREE.BufferAttribute;
    let vertex = 0;
    for (const meta of edgeMetaRef.current) {
      let lit = false;
      if (meta.childFileId !== undefined) {
        lit = playback.touchByFile.has(meta.childFileId);
      } else if (meta.childPath) {
        lit = litDirs.has(meta.childPath);
      }
      const tinted = lit ? EDGE_LIT : EDGE_BASE;
      for (let v = 0; v < meta.vertexCount; v++) {
        colorAttr.setXYZ(vertex++, tinted.r, tinted.g, tinted.b);
      }
    }
    colorAttr.needsUpdate = true;
  }, [city, layout, playback]);

  // selection marker follows the selected leaf
  useEffect(() => {
    const selection = selectionRef.current;
    if (!selection || !city || !layout) return;
    const file = selectedPath ? city.files.find((f) => f.path === selectedPath) : undefined;
    const pos = file ? layout.leaf.get(file.id) : undefined;
    if (pos) {
      selection.ring.position.set(pos.x, 0.1, pos.z);
      selection.beam.position.set(pos.x, 3, pos.z);
      selection.ring.visible = true;
      selection.beam.visible = true;
      // the inspector opens over the right edge; pan the leaf clear of it
      const camera = cameraRef.current;
      const controls = controlsRef.current;
      const canvas = rendererRef.current?.domElement;
      if (camera && controls && canvas) {
        controls.autoRotate = false;
        ensureVisible(
          camera,
          controls,
          new THREE.Vector3(pos.x, LEAF_Y, pos.z),
          canvas.clientWidth,
          canvas.clientHeight,
          INSPECTOR_RESERVED_PX
        );
      }
    } else {
      selection.ring.visible = false;
      selection.beam.visible = false;
    }
  }, [city, layout, selectedPath]);

  // trail arcs + firefly
  useEffect(() => {
    const trail = trailRef.current;
    if (!trail || !city || !layout) return;

    // playback resolves fileId with the same path key the citymap uses, so a
    // target still missing one has no leaf to land on
    const targetFiles = playback.recentTargets
      .map((target) => (target.fileId !== undefined ? city.files[target.fileId] : undefined))
      .filter((file): file is CityFile => Boolean(file && layout.leaf.get(file.id)));

    const firefly = fireflyRef.current;
    if (firefly) {
      const head = targetFiles[targetFiles.length - 1];
      if (head) {
        const pos = layout.leaf.get(head.id)!;
        firefly.position.set(pos.x, LEAF_Y + 1.7, pos.z);
        firefly.visible = true;
      } else {
        firefly.visible = false;
      }
    }

    trail.update(
      targetFiles.map((file) => {
        const pos = layout.leaf.get(file.id)!;
        return new THREE.Vector3(pos.x, LEAF_Y + 0.3, pos.z);
      })
    );
  }, [city, layout, playback]);

  return <div className="city-scene" ref={hostRef} aria-label="Firefly tree" />;
}
