"use client";

export default function InputBar({ value, onChange, onSend, disabled }: {
  value: string;
  onChange: (val: string) => void;
  onSend: () => void;
  disabled?: boolean;
}) {
  return (
    <div className="inputbar">
      <div className="inputwrap">
        <input
          className="input"
          placeholder={disabled ? "Working on it\u2026" : "Ask anything about your business..."}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && !disabled && onSend()}
          disabled={disabled}
        />
        <button className="sendbtn" onClick={onSend} disabled={disabled} style={{ opacity: disabled ? 0.5 : 1 }}>↑</button>
      </div>
    </div>
  );
}
