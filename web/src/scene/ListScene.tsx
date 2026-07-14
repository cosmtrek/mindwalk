import type { CityMap, Touch } from "../types";

export function ListScene({
  city,
  touchByPath,
  selectedPath,
  onSelect
}: {
  city?: CityMap;
  touchByPath: Map<string, Touch>;
  selectedPath?: string;
  onSelect: (path?: string) => void;
}) {
  return (
    <section className="list-scene" aria-label="Accessible repository file list">
      <header><strong>Repository files</strong><span>{city?.files.length ?? 0} mapped</span></header>
      <div>
        {(city?.files ?? []).map((file) => {
          const touch = touchByPath.get(file.path);
          return (
            <button
              key={file.path}
              className={selectedPath === file.path ? "active" : ""}
              onClick={() => onSelect(selectedPath === file.path ? undefined : file.path)}
            >
              <span className={`action-dot ${touch || "unvisited"}`} aria-hidden />
              <strong>{file.path}</strong>
              <span>{touch || "unvisited"} · {file.lines.toLocaleString()} lines</span>
            </button>
          );
        })}
      </div>
    </section>
  );
}
