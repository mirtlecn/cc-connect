export const idListClassName = 'font-mono overflow-x-auto';

export const parseLines = (value: string) => [...new Set(value.split('\n').map(line => line.trim()).filter(Boolean))];
