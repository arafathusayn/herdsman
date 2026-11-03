/** A gesture that starts and ends on a dialog's backdrop cancels it. */
export function dismissOnBackdrop(dialog: HTMLDialogElement): void {
  const outside = (event: MouseEvent): boolean => {
    const rect = dialog.getBoundingClientRect();
    return (
      event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom
    );
  };
  let pressedOutside = false;
  dialog.addEventListener("pointerdown", (event) => {
    pressedOutside = event.target === dialog && outside(event);
  });
  dialog.addEventListener("click", (event) => {
    if (pressedOutside && event.target === dialog && outside(event)) dialog.close("cancel");
    pressedOutside = false;
  });
}
