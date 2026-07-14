import { FolderSearch, FolderPlus, House, X } from "lucide-react";

export function RepositoryOnboarding({
  onScanHome,
  onChooseFolders,
  onAddManually,
  onSkip
}: {
  onScanHome: () => void;
  onChooseFolders: () => void;
  onAddManually: () => void;
  onSkip: () => void;
}) {
  return (
    <section className="onboarding-card repository-onboarding" aria-labelledby="repository-onboarding-title">
      <p className="eyebrow">First run</p>
      <h2 id="repository-onboarding-title">Add your first repository</h2>
      <p>
        Discovery stays off until you choose roots, review the exact scan plan, and start it. Nothing found is
        registered until you select and confirm it.
      </p>
      <div className="onboarding-actions">
        <button type="button" className="primary" onClick={onScanHome}>
          <House size={16} aria-hidden /> Scan my home folder
        </button>
        <button type="button" onClick={onChooseFolders}>
          <FolderSearch size={16} aria-hidden /> Choose folders
        </button>
        <button type="button" onClick={onAddManually}>
          <FolderPlus size={16} aria-hidden /> Add a path manually
        </button>
        <button type="button" onClick={onSkip}>
          <X size={16} aria-hidden /> Skip for now
        </button>
      </div>
    </section>
  );
}
