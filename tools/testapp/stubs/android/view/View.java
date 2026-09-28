package android.view;
public class View {
    public View(android.content.Context context) { throw new RuntimeException("Stub!"); }
    public void setOnClickListener(OnClickListener l) { throw new RuntimeException("Stub!"); }
    public void setPadding(int left, int top, int right, int bottom) { throw new RuntimeException("Stub!"); }
    public interface OnClickListener { void onClick(View v); }
    public void setOnTouchListener(OnTouchListener l) { throw new RuntimeException("Stub!"); }
    public interface OnTouchListener { boolean onTouch(View v, MotionEvent event); }
}
