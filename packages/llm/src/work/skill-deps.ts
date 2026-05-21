// Dependency manifest for Neko's bundled skills (packages/llm/assets/builtin-skills/*).
// The upstream Anthropic skills don't ship a machine-readable dep list — they
// mention requirements in SKILL.md prose. Mirroring those here lets the Dockerfile
// and scripts/skill-doctor.ts know what to install.
//
// User-authored skills are not in this manifest. They typically use the runtime's
// existing tools (Bash, Read/Write) rather than external Python/binary deps.

export interface SkillDeps {
  python: string[];
  pip: string[];
  binaries: string[];
  apt: string[];
  brew: string[];
}

export const KNOWN_SKILL_DEPS: Record<string, SkillDeps> = {
  xlsx: {
    python: ["openpyxl"],
    pip: ["openpyxl"],
    binaries: ["soffice"],
    apt: ["libreoffice"],
    brew: ["--cask libreoffice"],
  },
  pptx: {
    python: ["pptx", "PIL"],
    pip: ["python-pptx", "Pillow"],
    binaries: ["soffice"],
    apt: ["libreoffice"],
    brew: ["--cask libreoffice"],
  },
  docx: {
    python: ["docx"],
    pip: ["python-docx"],
    binaries: ["soffice"],
    apt: ["libreoffice"],
    brew: ["--cask libreoffice"],
  },
  pdf: {
    python: ["pypdf", "pdfplumber", "reportlab", "PIL"],
    pip: ["pypdf", "pdfplumber", "reportlab", "Pillow"],
    binaries: ["pdftotext", "qpdf"],
    apt: ["poppler-utils", "qpdf"],
    brew: ["poppler", "qpdf"],
  },
  "internal-comms": {
    python: [],
    pip: [],
    binaries: [],
    apt: [],
    brew: [],
  },
  "skill-creator": {
    python: ["yaml"],
    pip: ["PyYAML"],
    binaries: [],
    apt: [],
    brew: [],
  },
  // ─── Tier-A finance skills (Hermes-sourced, Apache-2.0) ────────────
  // All five model skills pair with excel-author and use openpyxl — same
  // dep set as the generic xlsx builtin, so no new pip/apt requirements
  // beyond what's already baked.
  "excel-author": {
    python: ["openpyxl"],
    pip: ["openpyxl"],
    binaries: [],
    apt: [],
    brew: [],
  },
  "pptx-author": {
    python: ["pptx", "PIL"],
    pip: ["python-pptx", "Pillow"],
    binaries: [],
    apt: [],
    brew: [],
  },
  "3-statement-model": {
    python: ["openpyxl"],
    pip: ["openpyxl"],
    binaries: [],
    apt: [],
    brew: [],
  },
  "dcf-model": {
    python: ["openpyxl"],
    pip: ["openpyxl"],
    binaries: [],
    apt: [],
    brew: [],
  },
  "lbo-model": {
    python: ["openpyxl"],
    pip: ["openpyxl"],
    binaries: [],
    apt: [],
    brew: [],
  },
  "comps-analysis": {
    python: ["openpyxl"],
    pip: ["openpyxl"],
    binaries: [],
    apt: [],
    brew: [],
  },
  "merger-model": {
    python: ["openpyxl"],
    pip: ["openpyxl"],
    binaries: [],
    apt: [],
    brew: [],
  },
  // ─── Tier-A devops procedural-knowledge skills ─────────────────────
  // Both are pure procedural prompts — no Python imports beyond stdlib,
  // no binaries beyond the worker's shell tools.
  watchers: {
    python: [],
    pip: [],
    binaries: [],
    apt: [],
    brew: [],
  },
  "webhook-subscriptions": {
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
  apt: string[];
  brewFormulas: string[];
  brewCasks: string[];
} {
  const pip = new Set<string>();
  const apt = new Set<string>();
  const brewFormulas = new Set<string>();
  const brewCasks = new Set<string>();
  for (const deps of Object.values(KNOWN_SKILL_DEPS)) {
    for (const pkg of deps.pip) pip.add(pkg);
    for (const pkg of deps.apt) apt.add(pkg);
    for (const entry of deps.brew) {
      if (entry.startsWith("--cask ")) brewCasks.add(entry.slice(7));
      else brewFormulas.add(entry);
    }
  }
  return {
    pip: [...pip],
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
