// What the bundled skills in packages/llm/assets/builtin-skills need. Keep
// recipes/sandbox/Dockerfile and scripts/skill-doctor.ts in step with it.

export interface SkillDeps {
  python: string[];
  pip: string[];
  node?: string[];
  npm?: string[];
  binaries: string[];
  apt: string[];
  brew: string[];
}

export const KNOWN_SKILL_DEPS: Record<string, SkillDeps> = {
  "document-extraction": {
    python: ["pypdf", "docx", "pptx", "openpyxl"],
    pip: ["pypdf", "python-docx", "python-pptx", "openpyxl"],
    binaries: ["pdftotext", "tesseract", "file"],
    apt: ["poppler-utils", "tesseract-ocr", "file"],
    brew: ["poppler", "tesseract"],
  },
  "skill-creator": {
    python: ["yaml"],
    pip: ["PyYAML"],
    binaries: [],
    apt: [],
    brew: [],
  },
  "graphjin-config": {
    python: [],
    pip: [],
    binaries: [],
    apt: [],
    brew: [],
  },
};

// Union of all pip / apt / brew deps across the manifest. Used by the
// Dockerfile-time installer to bake everything into the image, and by the
// dev-side doctor script to print fresh-machine setup commands.
export function aggregateSkillDeps(): {
  pip: string[];
  npm: string[];
  apt: string[];
  brewFormulas: string[];
  brewCasks: string[];
} {
  const pip = new Set<string>();
  const npm = new Set<string>();
  const apt = new Set<string>();
  const brewFormulas = new Set<string>();
  const brewCasks = new Set<string>();
  for (const deps of Object.values(KNOWN_SKILL_DEPS)) {
    for (const pkg of deps.pip) pip.add(pkg);
    for (const pkg of deps.npm ?? []) npm.add(pkg);
    for (const pkg of deps.apt) apt.add(pkg);
    for (const entry of deps.brew) {
      if (entry.startsWith("--cask ")) brewCasks.add(entry.slice(7));
      else brewFormulas.add(entry);
    }
  }
  return {
    pip: [...pip],
    npm: [...npm],
    apt: [...apt],
    brewFormulas: [...brewFormulas],
    brewCasks: [...brewCasks],
  };
}

/**
 * Synthesise a SkillDeps record from raw SKILL.md frontmatter — used
 * by the doctor for community skills installed under ~/.openneko/
 * skills/ that don't have a hand-maintained KNOWN_SKILL_DEPS entry.
 *
 * Only the binary-presence check matters here: we can't reliably map
 * a binary name (e.g. `blogwatcher-cli`) to an apt/brew package name,
 * so we record the binary for the OK/MISSING report and leave apt/brew
 * empty (operators install community-skill binaries manually).
 *
 * Python imports declared in skill prose aren't parsed — too varied
 * across community skills to extract reliably.
 */
export function synthesizeSkillDeps(prereq: {
  commands?: string[];
  envVars?: string[];
}): SkillDeps {
  return {
    python: [],
    pip: [],
    binaries: [...(prereq.commands ?? [])],
    apt: [],
    brew: [],
  };
}
