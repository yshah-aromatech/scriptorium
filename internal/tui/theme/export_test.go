package theme

// FromTintForTest exposes the tint adapter to the external test package
// without widening the real API: nothing outside tests builds palettes from a
// hand-made Tint.
var FromTintForTest = fromTint
