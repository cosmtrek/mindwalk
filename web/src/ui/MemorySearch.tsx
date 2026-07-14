import { BookOpen, Search, X } from "lucide-react";
import { useState } from "react";
import { searchMemories } from "../api/client";
import type { MemorySearchResult } from "../types";

export function MemorySearch() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<MemorySearchResult[]>([]);
  const [status, setStatus] = useState("Enter keywords to search the local FTS index.");

  return (
    <div className="memory-search">
      <button className="memory-toggle" onClick={() => setOpen((value) => !value)} aria-expanded={open}>
        <BookOpen size={14} aria-hidden /> Memory
      </button>
      {open ? (
        <section className="memory-panel" aria-label="Local memory search">
          <header>
            <div>
              <p className="eyebrow">Local second brain</p>
              <strong>Keyword search</strong>
            </div>
            <button className="icon-btn" onClick={() => setOpen(false)} aria-label="Close memory search"><X size={14} /></button>
          </header>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const value = query.trim();
              if (!value) return;
              setStatus("Searching…");
              void searchMemories(value)
                .then((next) => {
                  setResults(next);
                  setStatus(next.length ? `${next.length} result${next.length === 1 ? "" : "s"}` : "No matching local memories.");
                })
                .catch(() => {
                  setResults([]);
                  setStatus("Memory search is unavailable.");
                });
            }}
          >
            <Search size={14} aria-hidden />
            <input value={query} onChange={(event) => setQuery(event.currentTarget.value)} placeholder="Search memories" aria-label="Search local memories" />
          </form>
          <p className="memory-status" role="status">{status}</p>
          <div className="memory-results">
            {results.map(({ memory }) => (
              <article key={memory.memoryId}>
                <span>{memory.namespace}</span>
                <strong>{memory.title}</strong>
                <p>{memory.body}</p>
                <small>{memory.provenance.quality} · {memory.provenance.sourceName || memory.provenance.sourceType}</small>
              </article>
            ))}
          </div>
          <p className="memory-disclaimer">Local FTS retrieval only — this is not model training.</p>
        </section>
      ) : null}
    </div>
  );
}
