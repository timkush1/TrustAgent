/**
 * The Playground form, kept between runs and while the viewer visits other pages (SPEC §3.3: "the
 * form is kept between runs"). In memory only: prompts may hold content the viewer would not want
 * written to disk.
 */
import { create } from 'zustand';
import type { ChatMessage } from '@/types/api';
import { INITIAL_FORM, type EvidenceDoc, type PlaygroundForm } from './playgroundModel';

interface PlaygroundStore {
  form: PlaygroundForm;
  set: <K extends keyof PlaygroundForm>(key: K, value: PlaygroundForm[K]) => void;
  setMessage: (index: number, patch: Partial<ChatMessage>) => void;
  addMessage: () => void;
  removeMessage: (index: number) => void;
  setDoc: (index: number, patch: Partial<EvidenceDoc>) => void;
  addDoc: () => void;
  removeDoc: (index: number) => void;
  load: (form: PlaygroundForm) => void;
  reset: () => void;
}

export const usePlaygroundStore = create<PlaygroundStore>((set) => ({
  form: INITIAL_FORM,
  set: (key, value) => set((s) => ({ form: { ...s.form, [key]: value } })),
  setMessage: (index, patch) =>
    set((s) => ({ form: { ...s.form, messages: s.form.messages.map((m, i) => (i === index ? { ...m, ...patch } : m)) } })),
  addMessage: () =>
    set((s) => {
      const last = s.form.messages[s.form.messages.length - 1];
      const role: ChatMessage['role'] = last?.role === 'user' ? 'assistant' : 'user';
      return { form: { ...s.form, messages: [...s.form.messages, { role, content: '' }] } };
    }),
  removeMessage: (index) => set((s) => ({ form: { ...s.form, messages: s.form.messages.filter((_, i) => i !== index) } })),
  setDoc: (index, patch) =>
    set((s) => ({ form: { ...s.form, evidence: s.form.evidence.map((d, i) => (i === index ? { ...d, ...patch } : d)) } })),
  addDoc: () => set((s) => ({ form: { ...s.form, evidence: [...s.form.evidence, { id: '', text: '' }] } })),
  removeDoc: (index) => set((s) => ({ form: { ...s.form, evidence: s.form.evidence.filter((_, i) => i !== index) } })),
  load: (form) => set({ form: structuredClone(form) }),
  reset: () => set({ form: structuredClone(INITIAL_FORM) }),
}));
