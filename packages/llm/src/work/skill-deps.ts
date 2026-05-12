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
